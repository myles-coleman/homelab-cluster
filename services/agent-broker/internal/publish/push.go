// Package publish: the real pusher. It mints one contents:write token for the
// whole operation, materialises the thin bundle, and pushes exactly one commit to
// exactly one agent ref (spec 30, FR-35, FR-40).
package publish

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/gitbundle"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/refname"
)

// GitPusher pushes with a single installation token.
type GitPusher struct {
	Owner  string
	Opts   gitbundle.Options
	Tokens TokenSource
}

// Push fetches main history with the token, unbundles the agent commit, and
// pushes only refs/heads/<ref>. It never force-pushes and never pushes another
// ref.
func (g *GitPusher) Push(ctx context.Context, repo, ref, commitSHA string, bundle []byte) error {
	if err := refname.Validate(ref); err != nil {
		return fmt.Errorf("publish: invalid agent ref: %w", err)
	}
	if !validObjectID(commitSHA) {
		return fmt.Errorf("publish: commit id is not an object name")
	}
	if g.Tokens == nil {
		return fmt.Errorf("publish: token source is not configured")
	}

	// One token for the whole operation: read the history and push the branch with
	// the same credential (FR-40).
	tok, err := g.Tokens.InstallationToken(ctx, g.Owner, repo, map[string]string{"contents": "write"})
	if err != nil {
		return fmt.Errorf("publish: mint installation token: %w", err)
	}

	opts := g.Opts
	opts.Token = tok.Value
	tip, history, err := gitbundle.FetchHistory(ctx, opts, g.Owner, repo)
	if err != nil {
		return fmt.Errorf("publish: fetch history for %q: %w", repo, err)
	}
	if tip == "" || len(history) == 0 {
		return fmt.Errorf("publish: history for %q is unusable", repo)
	}

	gitBin := opts.GitBin
	if gitBin == "" {
		gitBin = "git"
	}
	workDir := opts.WorkDir
	if workDir == "" {
		workDir = os.TempDir()
	}

	scratch, err := os.MkdirTemp(workDir, "push-*")
	if err != nil {
		return fmt.Errorf("publish: create scratch directory: %w", err)
	}
	defer os.RemoveAll(scratch)

	repoDir := filepath.Join(scratch, "repo")
	historyFile := filepath.Join(scratch, "history.bundle")
	bundleFile := filepath.Join(scratch, "submitted.bundle")
	if err := os.WriteFile(historyFile, history, 0o600); err != nil {
		return fmt.Errorf("publish: write history bundle: %w", err)
	}
	if err := os.WriteFile(bundleFile, bundle, 0o600); err != nil {
		return fmt.Errorf("publish: write submitted bundle: %w", err)
	}

	if _, err := runGit(ctx, gitBin, scratch, nil, "init", "--quiet", repoDir); err != nil {
		return err
	}
	if _, err := runGit(ctx, gitBin, repoDir, nil,
		"-c", "protocol.file.allow=always", "fetch", "--quiet", historyFile, "HEAD"); err != nil {
		return err
	}
	tipOut, err := runGit(ctx, gitBin, repoDir, nil, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return err
	}
	if fetchedTip := strings.TrimSpace(tipOut); fetchedTip != tip {
		return fmt.Errorf("publish: publisher reported tip %s but the history bundle carries %s", tip, fetchedTip)
	}

	refspec := "refs/heads/" + ref + ":refs/agent/incoming"
	if _, err := runGit(ctx, gitBin, repoDir, nil,
		"-c", "protocol.file.allow=always", "fetch", "--quiet", bundleFile, refspec); err != nil {
		return fmt.Errorf("publish: materialise submitted bundle: %w", err)
	}
	commitOut, err := runGit(ctx, gitBin, repoDir, nil, "rev-parse", "refs/agent/incoming")
	if err != nil {
		return err
	}
	if got := strings.TrimSpace(commitOut); got != commitSHA {
		return fmt.Errorf("publish: bundle carries commit %s, not the verified %s", got, commitSHA)
	}

	baseURL := strings.TrimRight(opts.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://github.com"
	}
	repoURL := baseURL + "/" + g.Owner + "/" + repo + ".git"

	// The token reaches git through GIT_CONFIG_*, never argv (FR-40).
	env := tokenGitEnv(tok.Value)
	if _, err := runGit(ctx, gitBin, repoDir, env,
		"push", repoURL, commitSHA+":refs/heads/"+ref); err != nil {
		return fmt.Errorf("publish: push agent branch: %w", err)
	}
	return nil
}
