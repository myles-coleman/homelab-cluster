package gitbundle

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestFetchProducesClonableBundle exercises FR-13/FR-14 against a real git
// repository served over the file:// transport.
//
// The repository has two commits so the tip has a parent: that is the case where
// `git bundle create` inside a shallow clone produces a bundle no consumer can
// clone ("Failed to traverse parents"). The assertions therefore cover the
// consumer path, not just `git bundle verify`, which accepts a broken graph.
func TestFetchProducesClonableBundle(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git binary not available")
	}

	root := t.TempDir()
	const owner, repo = "myles-coleman", "nixos"
	repoDir := filepath.Join(root, owner, repo)
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("create repository directory: %v", err)
	}

	runGit(t, gitBin, repoDir, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write repository file: %v", err)
	}
	runGit(t, gitBin, repoDir, "add", "README.md")
	runGit(t, gitBin, repoDir, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "initial commit")

	if err := os.MkdirAll(filepath.Join(repoDir, "modules"), 0o755); err != nil {
		t.Fatalf("create nested directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "modules", "host.nix"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write nested file: %v", err)
	}
	runGit(t, gitBin, repoDir, "add", "modules/host.nix")
	runGit(t, gitBin, repoDir, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "second commit")

	head := strings.TrimSpace(runGit(t, gitBin, repoDir, "rev-parse", "HEAD"))
	wantTree := strings.TrimSpace(runGit(t, gitBin, repoDir, "rev-parse", "HEAD^{tree}"))

	mirrorDir := repoDir + ".git"
	if out, err := exec.Command(gitBin, "clone", "--mirror", repoDir, mirrorDir).CombinedOutput(); err != nil {
		t.Fatalf("create mirror repository: %v\n%s", err, out)
	}

	opts := Options{
		GitBin:  gitBin,
		BaseURL: "file://" + root,
		WorkDir: t.TempDir(),
	}

	commit, content, err := Fetch(context.Background(), opts, owner, repo)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if commit != head {
		t.Errorf("commit = %q, want %q (the real main tip, reported for base_commit)", commit, head)
	}
	if len(content) == 0 {
		t.Fatal("Fetch returned an empty bundle")
	}

	bundleFile := filepath.Join(t.TempDir(), "repo.bundle")
	if err := os.WriteFile(bundleFile, content, 0o600); err != nil {
		t.Fatalf("write bundle to temp file: %v", err)
	}
	if out, err := exec.Command(gitBin, "-C", repoDir, "bundle", "verify", bundleFile).CombinedOutput(); err != nil {
		t.Fatalf("git bundle verify rejected the returned bundle: %v\n%s", err, out)
	}

	// The consumer path: a plain clone, no --depth, no special flags. The agent
	// does exactly this inside the sandbox.
	cloneDir := filepath.Join(t.TempDir(), "consumer")
	if out, err := exec.Command(gitBin, "clone", bundleFile, cloneDir).CombinedOutput(); err != nil {
		t.Fatalf("consumer clone failed: %v\n%s", err, out)
	}

	if got := strings.TrimSpace(runGit(t, gitBin, cloneDir, "rev-list", "--count", "HEAD")); got != "1" {
		t.Errorf("clone has %s commits, want exactly 1", got)
	}
	if got := strings.TrimSpace(runGit(t, gitBin, cloneDir, "rev-parse", "HEAD^{tree}")); got != wantTree {
		t.Errorf("clone tree = %q, want %q (the main tip tree)", got, wantTree)
	}
	if log := runGit(t, gitBin, cloneDir, "log", "--oneline"); strings.Count(strings.TrimSpace(log), "\n") != 0 {
		t.Errorf("git log walks more than one commit:\n%s", log)
	}
	if body := runGit(t, gitBin, cloneDir, "cat-file", "-p", "HEAD"); strings.Contains(body, "\nparent ") {
		t.Errorf("bundled commit still names a parent the bundle does not contain:\n%s", body)
	}
	readme, err := os.ReadFile(filepath.Join(cloneDir, "README.md"))
	if err != nil {
		t.Fatalf("read cloned README: %v", err)
	}
	if string(readme) != "hello\n" {
		t.Errorf("README.md = %q, want %q", readme, "hello\n")
	}
	if _, err := os.Stat(filepath.Join(cloneDir, "modules", "host.nix")); err != nil {
		t.Errorf("nested file missing from the bundle tree: %v", err)
	}
}

func runGit(t *testing.T, gitBin, dir string, args ...string) string {
	t.Helper()

	cmd := exec.Command(gitBin, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
