package api

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
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/audit"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/bundle"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/identity"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/patch"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/policy"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/scan"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/policy"
)

const testSandboxUID = "4a22ef3b-312b-4624-8e64-9a3060579091"

type histStub struct {
	tip  string
	data []byte
}

func (h histStub) FetchHistory(context.Context, string) (string, []byte, error) {
	return h.tip, h.data, nil
}

func runGitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

type repoT struct {
	src, base, tip string
	bundle         []byte
}

func newRepoT(t *testing.T) repoT {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	runGitT(t, dir, "init", "-q", src)
	if err := os.MkdirAll(filepath.Join(src, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "docs", "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, src, "add", "-A")
	runGitT(t, src, "commit", "-q", "-m", "base")
	base := strings.TrimSpace(runGitT(t, src, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(src, "docs", "b.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, src, "add", "-A")
	runGitT(t, src, "commit", "-q", "-m", "tip")
	tip := strings.TrimSpace(runGitT(t, src, "rev-parse", "HEAD"))
	bundlePath := filepath.Join(dir, "history.bundle")
	runGitT(t, src, "bundle", "create", bundlePath, "HEAD")
	data, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	return repoT{src: src, base: base, tip: tip, bundle: data}
}

func patchFor(t *testing.T, r repoT) string {
	t.Helper()
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	runGitT(t, dir, "clone", "-q", r.src, work)
	runGitT(t, work, "checkout", "-q", "--detach", r.base)
	if err := os.WriteFile(filepath.Join(work, "docs", "a.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, work, "add", "-A")
	return runGitT(t, work, "diff", "--cached", "--binary")
}

// patchServer builds a Server whose identity resolves to a Sandbox with a real
// UUID, a working applier, and a buffer-backed audit log.
func patchServer(t *testing.T, r repoT) (*Server, *bytes.Buffer) {
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
				UID:        types.UID(testSandboxUID),
			}},
		},
	}
	sandbox := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": identity.SandboxAPIVersion,
		"kind":       identity.SandboxKind,
		"metadata": map[string]any{
			"name":      "opencode",
			"namespace": ns,
			"uid":       testSandboxUID,
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
	now := time.Date(2026, 11, 9, 1, 2, 3, 0, time.UTC)
	buf := &bytes.Buffer{}
	return &Server{
		Cache:      bundle.NewCache(stubFetcher{}, time.Minute),
		Resolver:   identity.NewResolver(fake.NewSimpleClientset(pod), dyn, ns),
		PolicyYAML: policyfs.YAML,
		Applier: &patch.Applier{
			WorkDir: t.TempDir(),
			History: histStub{tip: r.tip, data: r.bundle},
			Policy:  pol,
			Scanner: scan.New(),
			Now:     func() time.Time { return now },
		},
		Audit: audit.New(buf),
		Now:   func() time.Time { return now },
	}, buf
}

func liveRequest(t *testing.T, target string, body any, withCert bool) *http.Request {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	} else {
		rdr = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(http.MethodPost, target, rdr)
	if withCert {
		u, err := url.Parse(validSAN)
		if err != nil {
			t.Fatal(err)
		}
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{URIs: []*url.URL{u}}}}
	}
	return r
}

func sub(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	m := map[string]any{}
	for k, v := range body {
		m[k] = v
	}
	return m
}

func TestPatchesAcceptedShape(t *testing.T) {
	r := newRepoT(t)
	s, _ := patchServer(t, r)
	body := sub(t, map[string]any{}) // replace defaults below
	body["repo"] = "nixos"
	body["base_commit"] = r.base
	body["title"] = "change docs"
	body["body"] = "why"
	body["commit_message"] = "docs: change"
	body["patch"] = patchFor(t, r)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, liveRequest(t, "/v1/patches", body, true))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var keys []string
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{"bundle_sha256", "canonical_hash", "commit_sha", "tree_sha"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Errorf("keys = %v, want exactly %v", keys, want)
	}
}

func TestPatchesRejections(t *testing.T) {
	r := newRepoT(t)
	good := patchFor(t, r)
	base := func() map[string]any {
		return map[string]any{
			"repo":           "nixos",
			"base_commit":    r.base,
			"title":          "change",
			"body":           "why",
			"commit_message": "docs: change",
			"patch":          good,
		}
	}

	tests := []struct {
		name   string
		body   map[string]any
		cert   bool
		status int
		rule   string
	}{
		{name: "no certificate", body: base(), cert: false, status: 401, rule: "unauthenticated"},
		{name: "unknown repo", body: func() map[string]any { b := base(); b["repo"] = "terraform"; return b }(), cert: true, status: 404, rule: "unknown-repo"},
		{name: "stale base", body: func() map[string]any { b := base(); b["base_commit"] = strings.Repeat("b", 40); return b }(), cert: true, status: 409, rule: "stale-base"},
		{name: "invalid slug", body: func() map[string]any { b := base(); b["slug"] = "../evil"; return b }(), cert: true, status: 422, rule: "invalid-ref"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := patchServer(t, r)
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, liveRequest(t, "/v1/patches", tc.body, tc.cert))
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.status, rec.Body.String())
			}
			var got map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got["rule_id"] != tc.rule {
				t.Errorf("rule_id = %q, want %q", got["rule_id"], tc.rule)
			}
			if got["reason"] == "" {
				t.Error("reason is empty")
			}
		})
	}
}

func TestPatchesMalformedBody(t *testing.T) {
	r := newRepoT(t)
	s, _ := patchServer(t, r)

	for name, raw := range map[string]string{
		"not json":      "{",
		"unknown field": `{"repo":"nixos","nope":1}`,
		"trailing data": `{"repo":"nixos"} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/patches", strings.NewReader(raw))
			u, _ := url.Parse(validSAN)
			req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{URIs: []*url.URL{u}}}}
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "bad-request") {
				t.Errorf("body = %s, want bad-request", rec.Body.String())
			}
		})
	}
}

func TestPatchesAuditOneRecordPerRequest(t *testing.T) {
	r := newRepoT(t)
	s, buf := patchServer(t, r)

	bad := map[string]any{"repo": "nixos", "base_commit": strings.Repeat("c", 40), "patch": "x"}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, liveRequest(t, "/v1/patches", bad, true))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("audit lines = %d, want 1", len(lines))
	}
	var gotAudit struct {
		Decision     string   `json:"decision"`
		RuleID       string   `json:"rule_id"`
		Reason       string   `json:"reason"`
		RuleOutcomes []string `json:"rule_outcomes"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &gotAudit); err != nil {
		t.Fatalf("decode audit record: %v", err)
	}
	if gotAudit.Decision != "reject" || gotAudit.RuleID != "stale-base" {
		t.Errorf("audit record = %s", lines[0])
	}
	// FR-51: the record carries the reason and the rule outcome, not only the
	// terminal id. The wire body and the audit line must agree.
	if gotAudit.Reason == "" {
		t.Errorf("audit reason is empty: %s", lines[0])
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	if gotAudit.Reason != body["reason"] {
		t.Errorf("audit reason = %q, wire reason = %q", gotAudit.Reason, body["reason"])
	}
	if len(gotAudit.RuleOutcomes) != 1 || gotAudit.RuleOutcomes[0] != "stale-base" {
		t.Errorf("rule_outcomes = %v, want [stale-base]", gotAudit.RuleOutcomes)
	}
}

func TestPolicyEndpoint(t *testing.T) {
	r := newRepoT(t)
	s, _ := patchServer(t, r)

	rec := httptest.NewRecorder()
	noCert := httptest.NewRequest(http.MethodGet, "/v1/policy", nil)
	s.Handler().ServeHTTP(rec, noCert)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-cert status = %d, want 401", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/policy", nil)
	u, _ := url.Parse(validSAN)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{URIs: []*url.URL{u}}}}
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/yaml" {
		t.Errorf("content-type = %q", ct)
	}
	if !bytes.Equal(rec.Body.Bytes(), policyfs.YAML) {
		t.Error("policy body does not equal the embedded allowlist")
	}
}
