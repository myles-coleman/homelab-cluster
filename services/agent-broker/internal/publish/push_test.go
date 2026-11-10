package publish

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/gitbundle"
)

var _ Pusher = (*GitPusher)(nil)

type pushTokens struct {
	calls int
	owner string
	repo  string
	perms map[string]string
	value string
}

func (p *pushTokens) InstallationToken(_ context.Context, owner, repo string, perms map[string]string) (Token, error) {
	p.calls++
	p.owner = owner
	p.repo = repo
	p.perms = perms
	if p.value == "" {
		p.value = "ghs_push_supersecret_value"
	}
	return Token{Value: p.value}, nil
}

func newBareRemote(t *testing.T, f mainFixture) (root, remote string) {
	t.Helper()
	root = t.TempDir()
	remote = filepath.Join(root, "myles-coleman", "nixos.git")
	if err := os.MkdirAll(filepath.Dir(remote), 0o755); err != nil {
		t.Fatalf("mkdir remote parent: %v", err)
	}
	mustGit(t, root, "clone", "-q", "--bare", f.src, remote)
	return root, remote
}

func newPushPusher(t *testing.T, root string, tokens *pushTokens) *GitPusher {
	t.Helper()
	return &GitPusher{
		Owner: "myles-coleman",
		Opts: gitbundle.Options{
			GitBin:  "git",
			BaseURL: "file://" + root,
			WorkDir: t.TempDir(),
		},
		Tokens: tokens,
	}
}

// gitArgvRecorder installs a git wrapper that appends every argv to a file and
// then executes the real binary. The token may travel in the environment, but
// FR-40 forbids it from ever appearing in argv.
func gitArgvRecorder(t *testing.T) (bin, logPath string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git binary is not available")
	}
	dir := t.TempDir()
	bin = filepath.Join(dir, "git")
	logPath = filepath.Join(dir, "argv.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$ARGV_LOG\"\nexec \"$REAL_GIT\" \"$@\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write git wrapper: %v", err)
	}
	t.Setenv("ARGV_LOG", logPath)
	t.Setenv("REAL_GIT", realGit)
	return bin, logPath
}

func remoteRefs(t *testing.T, remote string) []string {
	t.Helper()
	out := mustGit(t, filepath.Dir(remote), "--git-dir", remote, "for-each-ref", "--format=%(refname)")
	var refs []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line != "" {
			refs = append(refs, line)
		}
	}
	sort.Strings(refs)
	return refs
}

// TestGitPusherPushesExactlyOneAgentRef covers FR-35/FR-40: one contents:write
// token, a push of exactly the requested commit to refs/heads/<ref>, no force,
// no other ref, and the token never in argv.
func TestGitPusherPushesExactlyOneAgentRef(t *testing.T) {
	f := newMainFixture(t)
	fx := newAgentFixture(t, f, defaultAgentOpts(t))
	root, remote := newBareRemote(t, f)

	tokens := &pushTokens{}
	bin, argvLog := gitArgvRecorder(t)
	p := newPushPusher(t, root, tokens)
	p.Opts.GitBin = bin

	if err := p.Push(context.Background(), "nixos", fx.ref, fx.commit, fx.bundle); err != nil {
		t.Fatalf("Push: %v", err)
	}

	if tokens.calls != 1 {
		t.Errorf("token mints = %d, want exactly 1", tokens.calls)
	}
	if tokens.owner != "myles-coleman" || tokens.repo != "nixos" {
		t.Errorf("token scope = %s/%s, want myles-coleman/nixos", tokens.owner, tokens.repo)
	}
	if len(tokens.perms) != 1 || tokens.perms["contents"] != "write" {
		t.Errorf("permissions = %v, want exactly {contents: write}", tokens.perms)
	}

	if got := strings.TrimSpace(mustGit(t, filepath.Dir(remote), "--git-dir", remote, "rev-parse", "refs/heads/"+fx.ref)); got != fx.commit {
		t.Errorf("remote ref points at %s, want %s", got, fx.commit)
	}
	wantRefs := []string{"refs/heads/" + fx.ref, "refs/heads/main"}
	if got := remoteRefs(t, remote); strings.Join(got, ",") != strings.Join(wantRefs, ",") {
		t.Errorf("remote refs = %v, want exactly %v", got, wantRefs)
	}

	argv, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatalf("read git argv log: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(argv)), "\n")
	if strings.Contains(string(argv), tokens.value) {
		t.Errorf("installation token appeared in git argv:\n%s", argv)
	}
	foundPush, foundRefspec := false, false
	for _, arg := range args {
		switch {
		case arg == "push":
			foundPush = true
		case arg == "--force", arg == "-f":
			t.Errorf("git push used force: %q", arg)
		case arg == fx.commit+":refs/heads/"+fx.ref:
			foundRefspec = true
		}
	}
	if !foundPush {
		t.Errorf("git wrapper saw no push command; argv:\n%s", argv)
	}
	if !foundRefspec {
		t.Errorf("git push did not use the exact refspec %s; argv:\n%s", fx.commit+":refs/heads/"+fx.ref, argv)
	}
}

// TestGitPusherNeverForcesExistingBranch covers FR-40's non-force rule from the
// other side: if the ref already exists at a non-descendant commit, the push
// must fail and leave the remote untouched. A force push would silently replace
// it.
func TestGitPusherNeverForcesExistingBranch(t *testing.T) {
	f := newMainFixture(t)
	fx := newAgentFixture(t, f, defaultAgentOpts(t))
	root, remote := newBareRemote(t, f)

	// main's tip and the agent commit both descend from base, so tip is not an
	// ancestor of the agent commit: updating an existing ref is non-fast-forward.
	mustGit(t, filepath.Dir(remote), "--git-dir", remote, "update-ref", "refs/heads/"+fx.ref, f.tip)

	p := newPushPusher(t, root, &pushTokens{})
	err := p.Push(context.Background(), "nixos", fx.ref, fx.commit, fx.bundle)
	if err == nil {
		t.Fatal("Push overwrote an existing non-descendant branch")
	}

	if got := strings.TrimSpace(mustGit(t, filepath.Dir(remote), "--git-dir", remote, "rev-parse", "refs/heads/"+fx.ref)); got != f.tip {
		t.Errorf("remote ref moved to %s, want it left at %s", got, f.tip)
	}
}
