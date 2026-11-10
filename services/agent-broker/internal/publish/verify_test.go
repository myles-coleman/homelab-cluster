package publish

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/commitmeta"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/rules"
)

var _ Verifier = (*GitVerifier)(nil)

type historyStub struct {
	tip    string
	bundle []byte
	err    error

	calls int
	repos []string
}

func (h *historyStub) FetchHistory(_ context.Context, repo string) (string, []byte, error) {
	h.calls++
	h.repos = append(h.repos, repo)
	return h.tip, h.bundle, h.err
}

var _ HistorySource = (*historyStub)(nil)

func newTestVerifier(t *testing.T, f mainFixture) (*GitVerifier, *historyStub) {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git binary is not available")
	}
	h := &historyStub{tip: f.tip, bundle: f.history}
	return &GitVerifier{GitBin: gitBin, WorkDir: t.TempDir(), History: h}, h
}

func wantRejection(t *testing.T, err error, ruleID string) {
	t.Helper()
	rej, ok := rules.From(err)
	if !ok {
		t.Fatalf("error = %v, want a *rules.Rejection with id %q", err, ruleID)
	}
	if rej.RuleID != ruleID {
		t.Fatalf("rule = %q (%s), want %q", rej.RuleID, rej.Reason, ruleID)
	}
	if strings.TrimSpace(rej.Reason) == "" {
		t.Error("rejection reason is empty")
	}
}

// TestVerifyAcceptsThinAgentBundle covers FR-33/FR-34/FR-35: the publisher
// re-derives the canonical hash from the bundle, reads the metadata trailer and
// the agent ref, and reports the changed paths without touching GitHub.
func TestVerifyAcceptsThinAgentBundle(t *testing.T) {
	f := newMainFixture(t)
	fx := newAgentFixture(t, f, defaultAgentOpts(t))
	v, history := newTestVerifier(t, f)

	got, err := v.Verify(context.Background(), fx.request("nixos"))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}

	if got.Repo != "nixos" {
		t.Errorf("Repo = %q, want nixos", got.Repo)
	}
	if got.Ref != fx.ref {
		t.Errorf("Ref = %q, want %q", got.Ref, fx.ref)
	}
	if got.BaseCommit != fx.base {
		t.Errorf("BaseCommit = %q, want %q", got.BaseCommit, fx.base)
	}
	if got.CommitSHA != fx.commit {
		t.Errorf("CommitSHA = %q, want %q", got.CommitSHA, fx.commit)
	}
	if got.TreeSHA != fx.tree {
		t.Errorf("TreeSHA = %q, want %q", got.TreeSHA, fx.tree)
	}
	if got.Title != fx.title || got.Body != fx.body {
		t.Errorf("metadata = (%q, %q), want (%q, %q)", got.Title, got.Body, fx.title, fx.body)
	}
	if got.BundleSHA256 != fx.bundleSHA {
		t.Errorf("BundleSHA256 = %q, want %q", got.BundleSHA256, fx.bundleSHA)
	}
	if got.CanonicalHash != fx.canonical {
		t.Errorf("CanonicalHash = %q, want %q", got.CanonicalHash, fx.canonical)
	}

	wantFiles := map[string]string{
		"docs/a.txt":         "M",
		"docs/c.txt":         "A",
		"docs/delete-me.txt": "D",
	}
	if len(got.Files) != len(wantFiles) {
		t.Fatalf("Files = %+v, want exactly %d changed paths", got.Files, len(wantFiles))
	}
	for _, change := range got.Files {
		if want, ok := wantFiles[change.Path]; !ok || want != change.Status {
			t.Errorf("unexpected change %q status %q (want %v)", change.Path, change.Status, wantFiles)
		}
	}

	if history.calls != 1 {
		t.Errorf("FetchHistory calls = %d, want 1", history.calls)
	}
	if len(history.repos) != 1 || history.repos[0] != "nixos" {
		t.Errorf("FetchHistory repos = %v, want [nixos]", history.repos)
	}
}

// TestVerifyRejectsBeforeMutatingOrFetching covers FR-34's ordering guarantee:
// a bundle whose bytes do not match bundle_sha256 is rejected offline, before
// the publisher materialises history or contacts GitHub.
func TestVerifyRejectsBeforeFetchingHistory(t *testing.T) {
	f := newMainFixture(t)
	fx := newAgentFixture(t, f, defaultAgentOpts(t))
	v, history := newTestVerifier(t, f)

	req := fx.request("nixos")
	req.BundleSHA256 = strings.Repeat("0", 64)

	_, err := v.Verify(context.Background(), req)
	wantRejection(t, err, rules.HashMismatch)
	if history.calls != 0 {
		t.Errorf("FetchHistory calls = %d, want 0 after an offline hash rejection", history.calls)
	}
}

// TestVerifyRejections covers FR-33/FR-34/FR-35's disagreement matrix. Every
// case is a bundle that is internally well-formed enough to reach the check it
// is meant to exercise.
func TestVerifyRejections(t *testing.T) {
	tests := []struct {
		name      string
		build     func(t *testing.T, f mainFixture) PublishRequest
		rule      string
		historyOK bool
	}{
		{
			name: "metadata trailer absent",
			build: func(t *testing.T, f mainFixture) PublishRequest {
				o := defaultAgentOpts(t)
				o.IncludeMeta = false
				fx := newAgentFixture(t, f, o)
				req := fx.request("nixos")
				req.CanonicalHash = strings.Repeat("0", 64)
				return req
			},
			rule:      rules.HashMismatch,
			historyOK: true,
		},
		{
			name: "metadata JSON malformed",
			build: func(t *testing.T, f mainFixture) PublishRequest {
				o := defaultAgentOpts(t)
				o.RawTrailer = commitmeta.Prefix + "{not json}"
				return newAgentFixture(t, f, o).request("nixos")
			},
			rule:      rules.HashMismatch,
			historyOK: true,
		},
		{
			name: "metadata repo disagrees with request",
			build: func(t *testing.T, f mainFixture) PublishRequest {
				o := defaultAgentOpts(t)
				o.Meta.Repo = "homelab-cluster" // request says nixos
				return newAgentFixture(t, f, o).request("nixos")
			},
			rule:      rules.HashMismatch,
			historyOK: true,
		},
		{
			name: "metadata ref is malformed",
			build: func(t *testing.T, f mainFixture) PublishRequest {
				o := defaultAgentOpts(t)
				o.Meta.Ref = "agent/../escape"
				o.BundleRefs = []string{testAgentRef(t)}
				return newAgentFixture(t, f, o).request("nixos")
			},
			rule:      rules.InvalidRef,
			historyOK: true,
		},
		{
			name: "canonical hash disagrees with parsed metadata",
			build: func(t *testing.T, f mainFixture) PublishRequest {
				fx := newAgentFixture(t, f, defaultAgentOpts(t))
				req := fx.request("nixos")
				req.CanonicalHash = strings.Repeat("0", 64)
				return req
			},
			rule:      rules.HashMismatch,
			historyOK: true,
		},
		{
			name: "tree disagrees with canonical hash",
			build: func(t *testing.T, f mainFixture) PublishRequest {
				good := newAgentFixture(t, f, defaultAgentOpts(t))
				o := defaultAgentOpts(t)
				o.Mutate = func(repo string) {
					writeTestFile(t, filepath.Join(repo, "docs", "different.txt"), "different\n")
				}
				other := newAgentFixture(t, f, o)
				req := other.request("nixos")
				// The bytes and their digest are valid for other, but the hash
				// still describes good's commit/tree.
				req.CanonicalHash = good.canonical
				return req
			},
			rule:      rules.HashMismatch,
			historyOK: true,
		},
		{
			name: "more than one new commit",
			build: func(t *testing.T, f mainFixture) PublishRequest {
				o := defaultAgentOpts(t)
				o.ExtraCommits = 1
				return newAgentFixture(t, f, o).request("nixos")
			},
			rule:      rules.HashMismatch,
			historyOK: true,
		},
		{
			name: "duplicate agent refs",
			build: func(t *testing.T, f mainFixture) PublishRequest {
				o := defaultAgentOpts(t)
				o.BundleRefs = []string{
					testAgentRef(t),
					"agent/11111111-1111-1111-1111-111111111111/other",
				}
				return newAgentFixture(t, f, o).request("nixos")
			},
			rule:      rules.HashMismatch,
			historyOK: true,
		},
		{
			name: "no agent ref",
			build: func(t *testing.T, f mainFixture) PublishRequest {
				o := defaultAgentOpts(t)
				o.BundleRefs = []string{} // only HEAD is bundled
				return newAgentFixture(t, f, o).request("nixos")
			},
			rule:      rules.HashMismatch,
			historyOK: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newMainFixture(t)
			req := tc.build(t, f)
			v, history := newTestVerifier(t, f)

			_, err := v.Verify(context.Background(), req)
			wantRejection(t, err, tc.rule)
			if tc.historyOK && history.calls == 0 {
				t.Error("FetchHistory was never called; the test did not reach the intended check")
			}
		})
	}
}
