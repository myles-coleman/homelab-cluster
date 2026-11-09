package patch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/canon"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/policy"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/rules"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/scan"
)

type fixture struct {
	src    string
	base   string
	tip    string
	bundle []byte
}

type stubHistory struct {
	tip  string
	data []byte
}

func (s stubHistory) FetchHistory(context.Context, string) (string, []byte, error) {
	return s.tip, s.data, nil
}

func gitEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	git(t, dir, "init", "-q", src)
	writeFile(t, filepath.Join(src, "docs/a.txt"), "one\n")
	writeFile(t, filepath.Join(src, "modules/foo.nix"), "{ }\n")
	writeFile(t, filepath.Join(src, "modules/base.nix"), "{ imports = [ ./foo.nix ]; }\n")
	writeFile(t, filepath.Join(src, "flake.lock"), "{\"nodes\":{}}\n")
	git(t, src, "add", "-A")
	git(t, src, "commit", "-q", "-m", "base")
	base := strings.TrimSpace(git(t, src, "rev-parse", "HEAD"))

	writeFile(t, filepath.Join(src, "docs/b.txt"), "two\n")
	git(t, src, "add", "-A")
	git(t, src, "commit", "-q", "-m", "tip")
	tip := strings.TrimSpace(git(t, src, "rev-parse", "HEAD"))

	bundlePath := filepath.Join(dir, "history.bundle")
	git(t, src, "bundle", "create", bundlePath, "HEAD")
	data, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}
	return fixture{src: src, base: base, tip: tip, bundle: data}
}

// makePatch checks out base in a scratch clone, applies mutate, and returns the
// staged diff as patch text.
func makePatch(t *testing.T, f fixture, mutate func(repo string)) string {
	t.Helper()
	dir := t.TempDir()
	repo := filepath.Join(dir, "work")
	git(t, dir, "clone", "-q", f.src, repo)
	git(t, repo, "checkout", "-q", "--detach", f.base)
	mutate(repo)
	git(t, repo, "add", "-A")
	return git(t, repo, "diff", "--cached", "--binary")
}

func newApplier(t *testing.T, f fixture) *Applier {
	t.Helper()
	p, err := policy.Embedded()
	if err != nil {
		t.Fatalf("embedded policy: %v", err)
	}
	return &Applier{
		WorkDir: t.TempDir(),
		History: stubHistory{tip: f.tip, data: f.bundle},
		Policy:  p,
		Scanner: scan.New(),
		Now:     func() time.Time { return time.Date(2026, 11, 9, 1, 2, 3, 0, time.UTC) },
	}
}

func request(f fixture, patchText string) Request {
	return Request{
		Repo:          "nixos",
		Ref:           "agent/4a22ef3b-312b-4624-8e64-9a3060579091/test-slug",
		BaseCommit:    f.base,
		Title:         "test change",
		Body:          "why",
		CommitMessage: "test: change",
		Patch:         []byte(patchText),
		SandboxUID:    "4a22ef3b-312b-4624-8e64-9a3060579091",
	}
}

func wantRule(t *testing.T, err error, ruleID string) {
	t.Helper()
	rej, ok := rules.From(err)
	if !ok {
		t.Fatalf("error = %v, want a *rules.Rejection with id %q", err, ruleID)
	}
	if rej.RuleID != ruleID {
		t.Fatalf("rule = %q (%s), want %q", rej.RuleID, rej.Reason, ruleID)
	}
}

func TestApplyAccepted(t *testing.T) {
	f := newFixture(t)
	patchText := makePatch(t, f, func(repo string) {
		writeFile(t, filepath.Join(repo, "docs/a.txt"), "one changed\n")
		writeFile(t, filepath.Join(repo, "docs/c.txt"), "three\n")
	})

	res, err := newApplier(t, f).Apply(context.Background(), request(f, patchText))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.CommitSHA) != 40 || len(res.TreeSHA) != 40 {
		t.Errorf("shas = %q/%q, want 40-hex", res.CommitSHA, res.TreeSHA)
	}
	if len(res.Bundle) == 0 {
		t.Fatal("empty bundle")
	}
	got := strings.Join(res.Files, ",")
	if !strings.Contains(got, "docs/a.txt") || !strings.Contains(got, "docs/c.txt") {
		t.Errorf("files = %v, want both changed paths", res.Files)
	}

	// The emitted bundle must apply on top of base in a repository that already
	// has the history.
	dir := t.TempDir()
	verify := filepath.Join(dir, "verify")
	git(t, dir, "init", "-q", verify)
	histFile := filepath.Join(dir, "history.bundle")
	if err := os.WriteFile(histFile, f.bundle, 0o600); err != nil {
		t.Fatalf("write history: %v", err)
	}
	git(t, verify, "-c", "protocol.file.allow=always", "fetch", "-q", histFile, "HEAD")
	outFile := filepath.Join(dir, "out.bundle")
	if err := os.WriteFile(outFile, res.Bundle, 0o600); err != nil {
		t.Fatalf("write out bundle: %v", err)
	}
	git(t, verify, "bundle", "verify", outFile)
	git(t, verify, "-c", "protocol.file.allow=always", "fetch", "-q", outFile, "HEAD")
	if got := strings.TrimSpace(git(t, verify, "rev-list", "--count", f.base+"..FETCH_HEAD")); got != "1" {
		t.Errorf("new commits = %s, want 1", got)
	}
	parent := strings.TrimSpace(git(t, verify, "rev-parse", "FETCH_HEAD^"))
	if parent != f.base {
		t.Errorf("parent = %s, want %s", parent, f.base)
	}
}

func TestApplyRejections(t *testing.T) {
	f := newFixture(t)

	tests := []struct {
		name  string
		mut   func(repo string)
		patch string
		rule  string
	}{
		{
			name: "denied path",
			mut:  func(repo string) { writeFile(t, filepath.Join(repo, "flake.nix"), "{}\n") },
			rule: rules.PathNotAllowed,
		},
		{
			name: "symlink",
			mut: func(repo string) {
				if err := os.Symlink("a.txt", filepath.Join(repo, "docs", "link")); err != nil {
					t.Fatalf("symlink: %v", err)
				}
			},
			rule: rules.ForbiddenTreeShape,
		},
		{
			name: "executable bit",
			mut: func(repo string) {
				if err := os.Chmod(filepath.Join(repo, "docs", "a.txt"), 0o755); err != nil {
					t.Fatalf("chmod: %v", err)
				}
			},
			rule: rules.ForbiddenTreeShape,
		},
		{
			name: "gitattributes",
			mut:  func(repo string) { writeFile(t, filepath.Join(repo, "docs", ".gitattributes"), "* text\n") },
			rule: rules.ForbiddenTreeShape,
		},
		{
			name: "lockfile",
			mut:  func(repo string) { writeFile(t, filepath.Join(repo, "flake.lock"), "{\"nodes\":{\"x\":{}}}\n") },
			rule: rules.LockfileChange,
		},
		{
			name: "imports inline",
			mut: func(repo string) {
				writeFile(t, filepath.Join(repo, "modules/foo.nix"), "{ imports = [ ./evil.nix ]; }\n")
			},
			rule: rules.ImportsInjection,
		},
		{
			name: "imports no spaces",
			mut:  func(repo string) { writeFile(t, filepath.Join(repo, "modules/foo.nix"), "{imports=[./evil.nix];}\n") },
			rule: rules.ImportsInjection,
		},
		{
			name: "imports multiline",
			mut: func(repo string) {
				writeFile(t, filepath.Join(repo, "modules/foo.nix"), "{\n  imports =\n    [ ./evil.nix ];\n}\n")
			},
			rule: rules.ImportsInjection,
		},
		{
			name: "secret in file",
			mut: func(repo string) {
				writeFile(t, filepath.Join(repo, "docs/leak.txt"), "-----BEGIN RSA PRIVATE KEY-----\nAAAA\n")
			},
			rule: rules.SecretDetected,
		},
		{
			name: "secret base64",
			mut: func(repo string) {
				writeFile(t, filepath.Join(repo, "docs/leak.txt"), "cGFzc3dvcmQgPSAiaHVudGVyMmh1bnRlcjIiCg==\n")
			},
			rule: rules.SecretDetected,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			patchText := makePatch(t, f, tc.mut)
			_, err := newApplier(t, f).Apply(context.Background(), request(f, patchText))
			wantRule(t, err, tc.rule)
		})
	}
}

func TestApplyStaleBase(t *testing.T) {
	f := newFixture(t)
	patchText := makePatch(t, f, func(repo string) {
		writeFile(t, filepath.Join(repo, "docs/a.txt"), "x\n")
	})

	t.Run("unknown commit id", func(t *testing.T) {
		req := request(f, patchText)
		req.BaseCommit = strings.Repeat("a", 40)
		_, err := newApplier(t, f).Apply(context.Background(), req)
		wantRule(t, err, rules.StaleBase)
	})

	t.Run("unrelated commit", func(t *testing.T) {
		// A commit that exists but is not in this repository's history. Its
		// content must differ, or identical trees and timestamps would produce
		// the same object id as the fixture and the case would be vacuous.
		dir := t.TempDir()
		other := filepath.Join(dir, "other")
		git(t, dir, "init", "-q", other)
		writeFile(t, filepath.Join(other, "unrelated.txt"), "other\n")
		git(t, other, "add", "-A")
		git(t, other, "commit", "-q", "-m", "unrelated")
		unrelated := strings.TrimSpace(git(t, other, "rev-parse", "HEAD"))

		req := request(f, patchText)
		req.BaseCommit = unrelated
		_, err := newApplier(t, f).Apply(context.Background(), req)
		wantRule(t, err, rules.StaleBase)
	})
}

func TestApplyBadPatch(t *testing.T) {
	f := newFixture(t)
	_, err := newApplier(t, f).Apply(context.Background(), request(f, "this is not a patch\n"))
	wantRule(t, err, rules.PatchApplyFailed)
}

func TestApplyMetadataSecret(t *testing.T) {
	f := newFixture(t)
	patchText := makePatch(t, f, func(repo string) {
		writeFile(t, filepath.Join(repo, "docs/a.txt"), "clean\n")
	})
	req := request(f, patchText)
	req.Body = "token = ghp_0123456789abcdefghijklmnopqrstuvwx"
	_, err := newApplier(t, f).Apply(context.Background(), req)
	wantRule(t, err, rules.SecretDetected)
}

func TestApplyNoResidueOnRejection(t *testing.T) {
	f := newFixture(t)
	patchText := makePatch(t, f, func(repo string) {
		writeFile(t, filepath.Join(repo, "flake.nix"), "{}\n")
	})
	applier := newApplier(t, f)
	if _, err := applier.Apply(context.Background(), request(f, patchText)); err == nil {
		t.Fatal("expected rejection")
	}
	entries, err := os.ReadDir(applier.WorkDir)
	if err != nil {
		t.Fatalf("read workdir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("workdir has %d leftover entries after rejection", len(entries))
	}
}

func TestApplyGuards(t *testing.T) {
	f := newFixture(t)
	patchText := makePatch(t, f, func(repo string) {
		writeFile(t, filepath.Join(repo, "docs/a.txt"), "x\n")
	})

	t.Run("unknown repo", func(t *testing.T) {
		req := request(f, patchText)
		req.Repo = "terraform"
		_, err := newApplier(t, f).Apply(context.Background(), req)
		wantRule(t, err, rules.UnknownRepo)
	})
	t.Run("invalid ref", func(t *testing.T) {
		req := request(f, patchText)
		req.Ref = "agent/../x"
		_, err := newApplier(t, f).Apply(context.Background(), req)
		wantRule(t, err, rules.InvalidRef)
	})
	t.Run("empty patch", func(t *testing.T) {
		req := request(f, "")
		_, err := newApplier(t, f).Apply(context.Background(), req)
		wantRule(t, err, rules.BadRequest)
	})
}

// TestApplyBundleHashIsDeterministic keeps the canonical hash honest across two
// identical submissions (FR-29).
func TestApplyBundleHashIsDeterministic(t *testing.T) {
	f := newFixture(t)
	patchText := makePatch(t, f, func(repo string) {
		writeFile(t, filepath.Join(repo, "docs/a.txt"), "same\n")
	})
	req := request(f, patchText)

	first, err := newApplier(t, f).Apply(context.Background(), req)
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	second, err := newApplier(t, f).Apply(context.Background(), req)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if first.CommitSHA != second.CommitSHA {
		t.Errorf("commit shas differ: %s vs %s", first.CommitSHA, second.CommitSHA)
	}
	h1, err := canon.Hash(req.Repo, req.Ref, first.CommitSHA, first.TreeSHA, req.Title, req.Body)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := canon.Hash(req.Repo, req.Ref, second.CommitSHA, second.TreeSHA, req.Title, req.Body)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Errorf("canonical hashes differ: %s vs %s", h1, h2)
	}
}
