package gitbundle

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestFetchProducesVerifiableBundle exercises FR-13/FR-14 against a real git
// repository served over the file:// transport. The returned bytes must be a
// bundle git itself accepts and the commit must be the tip that was cloned, not
// an invented SHA.
func TestFetchProducesVerifiableBundle(t *testing.T) {
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

	head := strings.TrimSpace(runGit(t, gitBin, repoDir, "rev-parse", "HEAD"))

	// Accept either <base>/<owner>/<repo> or <base>/<owner>/<repo>.git as the
	// clone URL; the spec only pins that BaseURL stands in for github.com.
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
		t.Errorf("commit = %q, want %q (git rev-parse HEAD)", commit, head)
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
