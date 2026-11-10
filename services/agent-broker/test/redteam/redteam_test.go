// Package redteam drives the committed red-team fixture corpus through the
// validator's own entrypoints (spec 30, FR-53/FR-54).
//
// Every fixture is a JSON file under fixtures/. TestRedTeam applies it with the
// same patch.Applier that POST /v1/patches calls, or — for the unauthenticated
// fixture — through api.Server.Handler() without a client certificate. The test
// asserts the fixture's expected_rule_id; a fixture with an empty
// expected_rule_id is the positive control and must be accepted.
//
// TestCorpusCompleteness is the gate that keeps the corpus honest: it fails when
// a named evasion class or a validator denial rule has no fixture at all.
package redteam

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/api"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/identity"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/patch"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/policy"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/rules"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/scan"
)

const sandboxUID = "4a22ef3b-312b-4624-8e64-9a3060579091"

// fixture is one corpus entry. The shape is the corpus contract (FR-53): add a
// field only when a new evasion genuinely cannot be expressed by the existing
// ones.
//
// Class is one of namedClasses for evasion fixtures. Fixtures that target a
// denial rule directly (unknown repo, stale base, bad request, ...) use a class
// naming that rule; TestCorpusCompleteness requires the named classes to be
// present and does not forbid these rule-focused names.
type fixture struct {
	Name           string   `json:"name"`
	Class          string   `json:"class"`
	ExpectedRuleID string   `json:"expected_rule_id"`
	Repo           string   `json:"repo"`
	Title          string   `json:"title"`
	Body           string   `json:"body"`
	CommitMessage  string   `json:"commit_message"`
	Ref            string   `json:"ref"`
	BaseCommit     string   `json:"base_commit,omitempty"`
	EmptyPatch     bool     `json:"empty_patch,omitempty"`
	RawPatch       string   `json:"raw_patch,omitempty"`
	Transport      string   `json:"transport,omitempty"`
	Changes        []change `json:"changes,omitempty"`
}

// change describes one tree mutation. Exactly one of Content, Symlink or
// Gitlink is used; Mode only applies to a regular file.
type change struct {
	Path    string `json:"path"`
	Content string `json:"content,omitempty"`
	Mode    string `json:"mode,omitempty"`
	Symlink string `json:"symlink,omitempty"`
	Gitlink string `json:"gitlink,omitempty"`
}

// history is the one base repository built for the whole TestRedTeam run.
type history struct {
	src    string
	base   string
	tip    string
	bundle []byte
}

type historyStub struct {
	tip  string
	data []byte
}

func (s historyStub) FetchHistory(context.Context, string) (string, []byte, error) {
	return s.tip, s.data, nil
}

// namedClasses is the full evasion-class list from FR-53. The task text says
// "13 named evasion classes" but its own enumeration contains 14: MIME/base64,
// hex, percent-encoding, unicode escapes, commit message, PR body, branch slug,
// .gitattributes, symlink, gitlink, mode change, path traversal, imports
// injection, and secret shapes. The fuller enumeration is authoritative, so
// completeness is asserted over all 14.
var namedClasses = []string{
	"encoding-mime-base64",
	"encoding-hex",
	"encoding-percent",
	"encoding-unicode",
	"commit-message",
	"pr-body",
	"branch-slug",
	"gitattributes",
	"symlink",
	"gitlink",
	"mode-change",
	"path-traversal",
	"imports-injection",
	"secret-shapes",
}

// denialRules are the validator rejections every corpus must exercise (FR-53,
// FR-54). They are the stable rule ids from rules.Rejection.
var denialRules = []string{
	rules.BadRequest,
	rules.UnknownRepo,
	rules.PathNotAllowed,
	rules.ForbiddenTreeShape,
	rules.ImportsInjection,
	rules.LockfileChange,
	rules.SecretDetected,
	rules.StaleBase,
	rules.InvalidRef,
	rules.PatchApplyFailed,
	rules.Unauthenticated,
}

func TestRedTeam(t *testing.T) {
	fixtures := loadFixtures(t)
	// One base repository per run: every fixture is a patch against the same
	// base and the same full-history bundle.
	h := newHistory(t)

	for _, fx := range fixtures {
		fx := fx
		t.Run(fx.Name, func(t *testing.T) {
			// FR-53 defines the corpus; FR-54 proves the expected rule id.
			// Encoding and secret-shape fixtures also exercise FR-25/FR-26.
			t.Logf("FR-53/FR-54: class=%s expected_rule_id=%q repo=%s",
				fx.Class, fx.ExpectedRuleID, fx.Repo)

			if fx.Transport != "" {
				if fx.Transport != "unauthenticated" {
					t.Fatalf("unknown transport %q", fx.Transport)
				}
				driveUnauthenticated(t, fx)
				return
			}
			driveApplier(t, h, fx)
		})
	}
}

// TestCorpusCompleteness keeps the fixture directory from silently losing the
// coverage FR-53 promises.
func TestCorpusCompleteness(t *testing.T) {
	fixtures := loadFixtures(t)

	byClass := map[string]int{}
	byRule := map[string]int{}
	names := map[string]bool{}
	positives := 0

	for _, fx := range fixtures {
		if strings.TrimSpace(fx.Name) == "" {
			t.Errorf("fixture %+v has no name", fx)
			continue
		}
		if names[fx.Name] {
			t.Errorf("duplicate fixture name %q", fx.Name)
		}
		names[fx.Name] = true
		if strings.TrimSpace(fx.Class) == "" {
			t.Errorf("fixture %q has no class", fx.Name)
		}
		byClass[fx.Class]++
		if fx.ExpectedRuleID == "" {
			positives++
		} else {
			byRule[fx.ExpectedRuleID]++
		}
	}

	for _, class := range namedClasses {
		if byClass[class] == 0 {
			t.Errorf("FR-53: no fixture for named evasion class %q", class)
		}
	}
	for _, ruleID := range denialRules {
		if byRule[ruleID] == 0 {
			t.Errorf("FR-53/FR-54: no fixture expects denial rule %q", ruleID)
		}
	}
	if positives == 0 {
		t.Error("FR-54: no positive-control fixture (expected_rule_id is empty)")
	}
}

// driveApplier runs one fixture through the verification entrypoint.
func driveApplier(t *testing.T, h history, fx fixture) {
	t.Helper()

	p, err := policy.Embedded()
	if err != nil {
		t.Fatalf("embedded policy: %v", err)
	}
	applier := &patch.Applier{
		GitBin:  "git",
		WorkDir: t.TempDir(),
		History: historyStub{tip: h.tip, data: h.bundle},
		Policy:  p,
		Scanner: scan.New(),
	}

	base := h.base
	if fx.BaseCommit != "" {
		base = fx.BaseCommit
	}
	req := patch.Request{
		Repo:          fx.Repo,
		Ref:           fx.Ref,
		BaseCommit:    base,
		Title:         fx.Title,
		Body:          fx.Body,
		CommitMessage: fx.CommitMessage,
		Patch:         buildPatch(t, h, fx),
		SandboxUID:    sandboxUID,
	}
	if fx.EmptyPatch {
		req.Patch = nil
	}

	res, err := applier.Apply(context.Background(), req)

	if fx.ExpectedRuleID == "" {
		if err != nil {
			t.Fatalf("positive control was rejected: %v", err)
		}
		if res.CommitSHA == "" {
			t.Fatal("positive control produced no commit")
		}
		return
	}

	if err == nil {
		t.Fatalf("fixture was accepted; want rule %q (class %s)", fx.ExpectedRuleID, fx.Class)
	}
	rej, ok := rules.From(err)
	if !ok {
		t.Fatalf("error is not a *rules.Rejection: %v", err)
	}
	if rej.RuleID != fx.ExpectedRuleID {
		t.Fatalf("rule = %q (%s), want %q", rej.RuleID, rej.Reason, fx.ExpectedRuleID)
	}
}

// driveUnauthenticated exercises the HTTP entrypoint with no TLS peer
// certificate. Authentication runs before any patch decoding, so the body only
// has to be valid JSON.
func driveUnauthenticated(t *testing.T, fx fixture) {
	t.Helper()

	body, err := json.Marshal(map[string]string{
		"repo":           fx.Repo,
		"base_commit":    fx.BaseCommit,
		"commit_message": fx.CommitMessage,
		"title":          fx.Title,
		"body":           fx.Body,
		"patch":          "unused: authentication is checked first",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/patches", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	// No TLS state at all: Handler().authenticate must classify this as an
	// unauthenticated client before touching the applier.
	(&api.Server{}).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", rec.Code, rec.Body.String())
	}
	var got struct {
		RuleID string `json:"rule_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode 401 body: %v", err)
	}
	if got.RuleID != fx.ExpectedRuleID {
		t.Fatalf("rule_id = %q, want %q", got.RuleID, fx.ExpectedRuleID)
	}
}

// buildPatch materialises the fixture's tree changes against base in a
// disposable clone and returns the staged binary diff. A fixture with raw_patch
// bypasses the tree entirely.
//
// The path-traversal fixture uses raw_patch because a path that escapes the
// worktree cannot be created through the tree API. Git itself rejects the path
// during `git apply`, before the tree-shape rule sees a change, so that fixture
// expects patch-apply-failed rather than forbidden-tree-shape.
func buildPatch(t *testing.T, h history, fx fixture) []byte {
	t.Helper()
	if fx.RawPatch != "" {
		return []byte(fx.RawPatch)
	}

	root := t.TempDir()
	work := filepath.Join(root, "work")
	git(t, root, "clone", "-q", h.src, work)
	git(t, work, "checkout", "-q", "--detach", h.base)

	for _, ch := range fx.Changes {
		full := filepath.Join(work, filepath.FromSlash(ch.Path))
		switch {
		case ch.Symlink != "":
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatalf("mkdir for symlink %s: %v", ch.Path, err)
			}
			if err := os.Symlink(ch.Symlink, full); err != nil {
				t.Fatalf("symlink %s: %v", ch.Path, err)
			}
		case ch.Gitlink != "":
			target := ch.Gitlink
			switch target {
			case "base":
				target = h.base
			case "tip":
				target = h.tip
			}
			// git add -A stages a missing path as a deletion, which would drop
			// the gitlink again. Keeping the (empty) directory in the worktree
			// makes the cache entry visible to add -A.
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatalf("mkdir for gitlink %s: %v", ch.Path, err)
			}
			git(t, work, "update-index", "--add", "--cacheinfo", "160000,"+target+","+ch.Path)
		default:
			writeFile(t, full, ch.Content)
			switch ch.Mode {
			case "", "100644":
			case "100755":
				if err := os.Chmod(full, 0o755); err != nil {
					t.Fatalf("chmod %s: %v", ch.Path, err)
				}
			default:
				t.Fatalf("fixture %q uses unsupported mode %q", fx.Name, ch.Mode)
			}
		}
	}

	git(t, work, "add", "-A")
	return []byte(git(t, work, "diff", "--cached", "--binary"))
}

// newHistory builds the base repository once per run: docs/a.txt at base, a
// second commit as tip, and a full-history bundle carrying both.
func newHistory(t *testing.T) history {
	t.Helper()

	root := t.TempDir()
	src := filepath.Join(root, "src")
	git(t, root, "init", "-q", src)

	writeFile(t, filepath.Join(src, "docs", "a.txt"), "base\n")
	git(t, src, "add", "-A")
	git(t, src, "commit", "-q", "-m", "base")
	base := strings.TrimSpace(git(t, src, "rev-parse", "HEAD"))

	writeFile(t, filepath.Join(src, "docs", "b.txt"), "tip\n")
	git(t, src, "add", "-A")
	git(t, src, "commit", "-q", "-m", "tip")
	tip := strings.TrimSpace(git(t, src, "rev-parse", "HEAD"))

	bundlePath := filepath.Join(root, "history.bundle")
	git(t, src, "bundle", "create", bundlePath, "HEAD")
	data, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatalf("read history bundle: %v", err)
	}

	return history{src: src, base: base, tip: tip, bundle: data}
}

func loadFixtures(t *testing.T) []fixture {
	t.Helper()

	files, err := filepath.Glob(filepath.Join("fixtures", "*.json"))
	if err != nil {
		t.Fatalf("glob fixtures: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no fixtures found under fixtures/")
	}
	sort.Strings(files)

	fixtures := make([]fixture, 0, len(files))
	for _, name := range files {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		var fx fixture
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&fx); err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		if dec.More() {
			t.Fatalf("%s has trailing JSON data", name)
		}
		fixtures = append(fixtures, fx)
	}
	return fixtures
}

// git runs git with a clean global/system config and the fixture identity. Every
// call gets -c user.name/-c user.email so no caller depends on ambient config.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	base := []string{"-c", "user.name=t", "-c", "user.email=t@t.invalid"}
	cmd := exec.Command("git", append(base, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(name), err)
	}
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// TestRedTeamHTTP drives the same corpus through the validator's HTTP entrypoint
// (api.Server.Handler), not only through patch.Applier. This is what proves the
// wire contract: the 4xx body's rule_id and the 202 acceptance shape are the
// values the agent actually sees (FR-54).
func TestRedTeamHTTP(t *testing.T) {
	fixtures := loadFixtures(t)
	h := newHistory(t)
	handler := newHTTPValidator(t, h)

	for _, fx := range fixtures {
		fx := fx
		t.Run(fx.Name, func(t *testing.T) {
			if fx.Transport != "" {
				if fx.Transport != "unauthenticated" {
					t.Fatalf("unknown transport %q", fx.Transport)
				}
				driveUnauthenticated(t, fx)
				return
			}

			base := h.base
			if fx.BaseCommit != "" {
				base = fx.BaseCommit
			}
			body := map[string]any{
				"repo":           fx.Repo,
				"base_commit":    base,
				"commit_message": fx.CommitMessage,
				"title":          fx.Title,
				"body":           fx.Body,
				"patch":          string(buildPatch(t, h, fx)),
				"slug":           slugFromRef(fx.Ref),
			}
			if fx.EmptyPatch {
				body["patch"] = ""
			}

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httpRequestWithIdentity(t, body))

			if fx.ExpectedRuleID == "" {
				if rec.Code != http.StatusAccepted {
					t.Fatalf("positive control status = %d (body %s)", rec.Code, rec.Body.String())
				}
				var got struct {
					CommitSHA string `json:"commit_sha"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("decode 202 body: %v", err)
				}
				if got.CommitSHA == "" {
					t.Fatal("positive control returned no commit_sha")
				}
				return
			}

			if rec.Code < 400 || rec.Code >= 500 {
				t.Fatalf("status = %d, want 4xx (body %s)", rec.Code, rec.Body.String())
			}
			var got struct {
				RuleID string `json:"rule_id"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			if got.RuleID != fx.ExpectedRuleID {
				t.Fatalf("rule_id = %q, want %q (body %s)", got.RuleID, fx.ExpectedRuleID, rec.Body.String())
			}
		})
	}
}

// newHTTPValidator builds the real handler with a fake identity resolver and the
// real patch pipeline. The pod/Sandbox pair is the same shape the API tests use,
// so identity resolution is exercised rather than stubbed.
func newHTTPValidator(t *testing.T, h history) http.Handler {
	t.Helper()
	const ns = "agent-sandbox"

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "opencode",
			Namespace: ns,
			UID:       "pod-uid",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: identity.SandboxAPIVersion,
				Kind:       identity.SandboxKind,
				Name:       "opencode",
				UID:        types.UID(sandboxUID),
			}},
		},
	}
	sandbox := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": identity.SandboxAPIVersion,
		"kind":       identity.SandboxKind,
		"metadata": map[string]any{
			"name":      "opencode",
			"namespace": ns,
			"uid":       sandboxUID,
		},
	}}
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	gvr := schema.GroupVersionResource{Group: "agents.x-k8s.io", Version: "v1beta1", Resource: "sandboxes"}
	if _, err := dyn.Resource(gvr).Namespace(ns).Create(context.Background(), sandbox, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Embedded()
	if err != nil {
		t.Fatal(err)
	}

	return (&api.Server{
		Resolver: identity.NewResolver(fake.NewSimpleClientset(pod), dyn, ns),
		Applier: &patch.Applier{
			GitBin:  "git",
			WorkDir: t.TempDir(),
			History: historyStub{tip: h.tip, data: h.bundle},
			Policy:  pol,
			Scanner: scan.New(),
		},
	}).Handler()
}

func httpRequestWithIdentity(t *testing.T, body any) *http.Request {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/patches", bytes.NewReader(raw))
	u, err := url.Parse("spiffe://cowlab.org/ns/agent-sandbox/pod/opencode")
	if err != nil {
		t.Fatalf("parse SAN: %v", err)
	}
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{URIs: []*url.URL{u}}}}
	return r
}

// slugFromRef keeps the agent ref's slug component, including any separators, so
// the branch-slug fixture still reaches refname.Validate through the handler.
func slugFromRef(ref string) string {
	parts := strings.SplitN(ref, "/", 3)
	if len(parts) == 3 {
		return parts[2]
	}
	return ref
}
