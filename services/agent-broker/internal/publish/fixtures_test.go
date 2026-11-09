package publish

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/canon"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/commitmeta"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/refname"
)

const (
	testAgentUID  = "4a22ef3b-312b-4624-8e64-9a3060579091"
	testAgentSlug = "test-slug"
)

func testAgentRef(t *testing.T) string {
	t.Helper()
	ref, err := refname.Build(testAgentUID, testAgentSlug)
	if err != nil {
		t.Fatalf("build test ref: %v", err)
	}
	return ref
}

func publishGitEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
	)
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = publishGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// mainFixture is a two-commit main branch. base is the commit the validator
// applies a patch to; tip is the current main tip, which may be ahead of base.
type mainFixture struct {
	root    string
	src     string
	base    string
	tip     string
	history []byte
}

func newMainFixture(t *testing.T) mainFixture {
	t.Helper()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	mustGit(t, root, "init", "-q", "-b", "main", src)

	writeTestFile(t, filepath.Join(src, "docs", "a.txt"), "one\n")
	writeTestFile(t, filepath.Join(src, "docs", "delete-me.txt"), "bye\n")
	writeTestFile(t, filepath.Join(src, "modules", "foo.nix"), "{ }\n")
	mustGit(t, src, "add", "-A")
	mustGit(t, src, "commit", "-q", "-m", "base")
	base := strings.TrimSpace(mustGit(t, src, "rev-parse", "HEAD"))

	writeTestFile(t, filepath.Join(src, "docs", "tip.txt"), "tip\n")
	mustGit(t, src, "add", "-A")
	mustGit(t, src, "commit", "-q", "-m", "tip")
	tip := strings.TrimSpace(mustGit(t, src, "rev-parse", "HEAD"))

	bundlePath := filepath.Join(root, "history.bundle")
	mustGit(t, src, "bundle", "create", bundlePath, "HEAD")
	history, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatalf("read history bundle: %v", err)
	}
	return mainFixture{root: root, src: src, base: base, tip: tip, history: history}
}

type agentOpts struct {
	Meta         commitmeta.Metadata
	IncludeMeta  bool
	BundleRefs   []string // nil means [Meta.Ref]; an empty non-nil slice emits no refs
	Mutate       func(repo string)
	Message      string
	RawTrailer   string // when set, used verbatim instead of Encode(Meta)
	ExtraCommits int
}

// agentFixture is a validator-style thin bundle: exactly one new commit on top
// of mainFixture.base, exposed under refs/heads/<ref> and HEAD, with ^base as
// the bundle prerequisite.
type agentFixture struct {
	ref       string
	commit    string
	tree      string
	base      string
	title     string
	body      string
	canonical string
	bundleSHA string
	bundle    []byte
	message   string
}

func defaultAgentOpts(t *testing.T) agentOpts {
	t.Helper()
	return agentOpts{
		Meta: commitmeta.Metadata{
			Repo:  "nixos",
			Ref:   testAgentRef(t),
			Title: "test change",
			Body:  "why",
		},
		IncludeMeta: true,
		Mutate: func(repo string) {
			writeTestFile(t, filepath.Join(repo, "docs", "a.txt"), "changed\n")
			writeTestFile(t, filepath.Join(repo, "docs", "c.txt"), "new\n")
			if err := os.Remove(filepath.Join(repo, "docs", "delete-me.txt")); err != nil {
				t.Fatalf("remove fixture file: %v", err)
			}
		},
		Message: "test: change",
	}
}

func newAgentFixture(t *testing.T, f mainFixture, o agentOpts) agentFixture {
	t.Helper()

	work := filepath.Join(t.TempDir(), "work")
	mustGit(t, filepath.Dir(work), "clone", "-q", f.src, work)
	mustGit(t, work, "checkout", "-q", "--detach", f.base)

	if o.Mutate != nil {
		o.Mutate(work)
	} else {
		writeTestFile(t, filepath.Join(work, "docs", "c.txt"), "new\n")
	}
	mustGit(t, work, "add", "-A")

	message := o.Message
	if message == "" {
		message = "test: change"
	}
	switch {
	case o.RawTrailer != "":
		message += "\n\n" + o.RawTrailer
	case o.IncludeMeta:
		trailer, err := commitmeta.Encode(o.Meta)
		if err != nil {
			t.Fatalf("encode metadata: %v", err)
		}
		message += "\n\n" + trailer
	}
	mustGit(t, work, "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null",
		"commit", "-q", "-m", message)

	for i := 0; i < o.ExtraCommits; i++ {
		writeTestFile(t, filepath.Join(work, "docs", "extra-"+string(rune('a'+i))+".txt"), "extra\n")
		mustGit(t, work, "add", "-A")
		mustGit(t, work, "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null",
			"commit", "-q", "-m", "extra commit")
	}

	commit := strings.TrimSpace(mustGit(t, work, "rev-parse", "HEAD"))
	tree := strings.TrimSpace(mustGit(t, work, "rev-parse", "HEAD^{tree}"))

	refs := o.BundleRefs
	if refs == nil {
		refs = []string{o.Meta.Ref}
	}
	bundleArgs := []string{"bundle", "create", filepath.Join(filepath.Dir(work), "out.bundle")}
	for _, ref := range refs {
		mustGit(t, work, "update-ref", "refs/heads/"+ref, commit)
		bundleArgs = append(bundleArgs, "refs/heads/"+ref)
	}
	bundleArgs = append(bundleArgs, "HEAD", "^"+f.base)
	mustGit(t, work, bundleArgs...)

	bundlePath := filepath.Join(filepath.Dir(work), "out.bundle")
	bundle, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatalf("read agent bundle: %v", err)
	}

	fx := agentFixture{
		ref:       o.Meta.Ref,
		commit:    commit,
		tree:      tree,
		base:      f.base,
		title:     o.Meta.Title,
		body:      o.Meta.Body,
		bundleSHA: canon.BundleSHA256(bundle),
		bundle:    bundle,
		message:   message,
	}
	if o.IncludeMeta && o.RawTrailer == "" {
		fx.canonical, err = canon.Hash(o.Meta.Repo, o.Meta.Ref, commit, tree, o.Meta.Title, o.Meta.Body)
		if err != nil {
			t.Fatalf("canonical hash: %v", err)
		}
	}
	return fx
}

func (a agentFixture) request(repo string) PublishRequest {
	return PublishRequest{
		Repo:          repo,
		Bundle:        a.bundle,
		BundleSHA256:  a.bundleSHA,
		CanonicalHash: a.canonical,
	}
}
