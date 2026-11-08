// Package gitbundle produces a shallow git bundle from a GitHub repository using
// an installation token (spec 30, FR-13, FR-14).
//
// The bundle contains a single commit at the main tip plus its full tree: the
// clone is depth 1, so the commit has no parents and the bundle cannot leak
// history. The token is passed through the environment, never argv, and never
// written to disk or logs.
package gitbundle

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	// defaultBaseURL stands in for github.com when the caller does not override it.
	defaultBaseURL = "https://github.com"
	// maxBundleSize caps the bundle read into memory.
	maxBundleSize = 64 << 20 // 64 MiB
)

// Options controls one fetch. Zero values take safe defaults.
type Options struct {
	// GitBin is the git executable; defaults to "git".
	GitBin string
	// BaseURL is the HTTPS base, defaults to "https://github.com".
	BaseURL string
	// WorkDir is a writable scratch directory (the caller owns cleanup).
	WorkDir string
	// Token is a GitHub installation token with contents:read on the repo.
	Token string
}

// Fetch clones owner/repo at the main tip and returns the commit SHA and the git
// bundle bytes.
func Fetch(ctx context.Context, opts Options, owner, repo string) (commit string, content []byte, err error) {
	if !validPathComponent(owner) || !validPathComponent(repo) {
		return "", nil, fmt.Errorf("gitbundle: invalid repository %q/%q", owner, repo)
	}

	gitBin := opts.GitBin
	if gitBin == "" {
		gitBin = "git"
	}
	baseURL := strings.TrimRight(opts.BaseURL, "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	// GitHub rejects anonymous clones of private repositories, and sending the
	// request without a token would only produce a confusing 404 (FR-13).
	if isHTTPS(baseURL) && opts.Token == "" {
		return "", nil, errors.New("gitbundle: an installation token is required for an https base URL")
	}

	scratchRoot, cleanupRoot, err := ensureScratch(opts.WorkDir)
	if err != nil {
		return "", nil, err
	}
	defer cleanupRoot()

	scratch, err := os.MkdirTemp(scratchRoot, "fetch-*")
	if err != nil {
		return "", nil, fmt.Errorf("gitbundle: create scratch directory under %q: %w", scratchRoot, err)
	}
	// Removes the clone and the bundle on success and every failure path.
	defer os.RemoveAll(scratch)

	cloneDir := filepath.Join(scratch, "repo")
	bundleFile := filepath.Join(scratch, "main.bundle")
	env := gitEnv(opts.Token)
	repoURL := baseURL + "/" + owner + "/" + repo + ".git"

	// Depth 1 and a single branch keep the bundle to one commit with no history.
	if _, err := runGitCommand(ctx, gitBin, scratch, env, opts.Token,
		"clone", "--depth", "1", "--single-branch", "--branch", "main", repoURL, cloneDir); err != nil {
		return "", nil, err
	}
	if _, err := runGitCommand(ctx, gitBin, cloneDir, env, opts.Token,
		"bundle", "create", bundleFile, "HEAD"); err != nil {
		return "", nil, err
	}
	out, err := runGitCommand(ctx, gitBin, cloneDir, env, opts.Token, "rev-parse", "HEAD")
	if err != nil {
		return "", nil, err
	}
	commit = strings.TrimSpace(string(out))
	if commit == "" {
		return "", nil, errors.New("gitbundle: git rev-parse returned an empty commit")
	}

	info, err := os.Stat(bundleFile)
	if err != nil {
		return "", nil, fmt.Errorf("gitbundle: stat bundle: %w", err)
	}
	if info.Size() > maxBundleSize {
		return "", nil, fmt.Errorf("gitbundle: bundle is %d bytes, over the %d-byte limit", info.Size(), maxBundleSize)
	}
	content, err = os.ReadFile(bundleFile)
	if err != nil {
		return "", nil, fmt.Errorf("gitbundle: read bundle: %w", err)
	}
	return commit, content, nil
}

// ensureScratch returns the parent directory for the per-fetch scratch dir and a
// cleanup function. When the caller supplies a WorkDir its contents belong to the
// caller; only a directory this function created is removed wholesale.
func ensureScratch(workDir string) (string, func(), error) {
	if workDir == "" {
		dir, err := os.MkdirTemp("", "agent-broker-gitbundle-*")
		if err != nil {
			return "", nil, fmt.Errorf("gitbundle: create scratch root: %w", err)
		}
		return dir, func() { _ = os.RemoveAll(dir) }, nil
	}
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return "", nil, fmt.Errorf("gitbundle: prepare work directory %q: %w", workDir, err)
	}
	return workDir, func() {}, nil
}

// gitEnv assembles the child environment. The installation token reaches git
// through http.extraheader via the GIT_CONFIG_* variables, so it never appears in
// argv, the clone's config, or an error string (FR-13).
func gitEnv(token string) []string {
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if token == "" {
		return env
	}
	// GitHub's smart-HTTP endpoint authenticates App installation tokens as the
	// x-access-token user.
	credential := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	return append(env,
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.extraheader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic "+credential,
	)
}

// runGitCommand runs one git command and returns its stdout. Errors carry the command
// name and a token-redacted stderr excerpt, never the token itself.
func runGitCommand(ctx context.Context, gitBin, dir string, env []string, token string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, gitBin, args...)
	cmd.Dir = dir
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(redact(stderr.String(), token))
		if detail != "" {
			return nil, fmt.Errorf("gitbundle: git %s: %w: %s", args[0], err, detail)
		}
		return nil, fmt.Errorf("gitbundle: git %s: %w", args[0], err)
	}
	return stdout.Bytes(), nil
}

// redact removes both the raw token and the encoded credential it is sent as.
func redact(s, token string) string {
	if token == "" {
		return s
	}
	s = strings.ReplaceAll(s, token, "[REDACTED]")
	credential := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	return strings.ReplaceAll(s, credential, "[REDACTED]")
}

// validPathComponent keeps owner and repo from reshaping the URL or the local
// clone path.
func validPathComponent(s string) bool {
	return s != "" && s != "." && s != ".." &&
		!strings.ContainsAny(s, "/\\\x00")
}

func isHTTPS(baseURL string) bool {
	return strings.HasPrefix(strings.ToLower(baseURL), "https://")
}

var ErrNotImplemented = errors.New("gitbundle: not implemented")
