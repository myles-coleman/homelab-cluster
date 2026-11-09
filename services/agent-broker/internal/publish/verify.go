// Package publish: the real verifier. It performs every check the design puts
// before a mutating GitHub call (spec 30, FR-33, FR-34, FR-35).
package publish

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/canon"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/commitmeta"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/refname"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/rules"
)

// maxHistoryBundle bounds the history the verifier materialises.
const maxHistoryBundle = 512 << 20

// HistorySource fetches the main tip together with its full history.
type HistorySource interface {
	FetchHistory(ctx context.Context, repo string) (tip string, bundle []byte, err error)
}

// GitVerifier verifies a submitted thin bundle against real git objects.
type GitVerifier struct {
	GitBin  string
	WorkDir string
	History HistorySource
}

// Verify runs the offline hash check first, then materialises history and the
// submitted bundle and re-derives every value the canonical hash covers.
func (v *GitVerifier) Verify(ctx context.Context, req PublishRequest) (Verified, error) {
	if !ValidRepo(req.Repo) {
		return Verified{}, rules.New(rules.UnknownRepo, "repository is not served")
	}

	// Offline check: no fetch, no GitHub call, no scratch directory.
	if canon.BundleSHA256(req.Bundle) != req.BundleSHA256 {
		return Verified{}, rules.New(rules.HashMismatch, "bundle bytes do not match bundle_sha256")
	}

	if v.History == nil {
		return Verified{}, fmt.Errorf("publish: history source is not configured")
	}
	tip, history, err := v.History.FetchHistory(ctx, req.Repo)
	if err != nil {
		return Verified{}, fmt.Errorf("publish: fetch history for %q: %w", req.Repo, err)
	}
	if tip == "" || len(history) == 0 || len(history) > maxHistoryBundle {
		return Verified{}, fmt.Errorf("publish: history for %q is unusable", req.Repo)
	}

	gitBin := v.GitBin
	if gitBin == "" {
		gitBin = "git"
	}
	workDir := v.WorkDir
	if workDir == "" {
		workDir = os.TempDir()
	}

	scratch, err := os.MkdirTemp(workDir, "verify-*")
	if err != nil {
		return Verified{}, fmt.Errorf("publish: create scratch directory: %w", err)
	}
	defer os.RemoveAll(scratch)

	repoDir := filepath.Join(scratch, "repo")
	historyFile := filepath.Join(scratch, "history.bundle")
	bundleFile := filepath.Join(scratch, "submitted.bundle")
	if err := os.WriteFile(historyFile, history, 0o600); err != nil {
		return Verified{}, fmt.Errorf("publish: write history bundle: %w", err)
	}
	if err := os.WriteFile(bundleFile, req.Bundle, 0o600); err != nil {
		return Verified{}, fmt.Errorf("publish: write submitted bundle: %w", err)
	}

	if _, err := runGit(ctx, gitBin, scratch, nil, "init", "--quiet", repoDir); err != nil {
		return Verified{}, err
	}
	if _, err := runGit(ctx, gitBin, repoDir, nil,
		"-c", "protocol.file.allow=always", "fetch", "--quiet", historyFile, "HEAD"); err != nil {
		return Verified{}, err
	}
	tipOut, err := runGit(ctx, gitBin, repoDir, nil, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return Verified{}, err
	}
	if fetchedTip := strings.TrimSpace(tipOut); fetchedTip != tip {
		return Verified{}, fmt.Errorf("publish: publisher reported tip %s but the history bundle carries %s", tip, fetchedTip)
	}

	headsOut, err := runGit(ctx, gitBin, repoDir, nil, "bundle", "list-heads", bundleFile)
	if err != nil {
		return Verified{}, rules.New(rules.HashMismatch, "the submitted bundle is not a readable git bundle")
	}

	var agentRefs []string
	for _, line := range strings.Split(strings.TrimSpace(headsOut), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		name := fields[1]
		if !strings.HasPrefix(name, "refs/heads/agent/") {
			continue
		}
		bare := strings.TrimPrefix(name, "refs/heads/")
		if err := refname.Validate(bare); err != nil {
			return Verified{}, rules.New(rules.InvalidRef, "ref must be agent/<sandbox-uid>/<slug>")
		}
		agentRefs = append(agentRefs, bare)
	}
	if len(agentRefs) != 1 {
		return Verified{}, rules.New(rules.HashMismatch, "the bundle must carry exactly one agent ref")
	}
	ref := agentRefs[0]

	// Fetch the agent ref from the thin bundle. History, including the bundle's
	// prerequisite, is already present.
	refspec := "refs/heads/" + ref + ":refs/agent/incoming"
	if _, err := runGit(ctx, gitBin, repoDir, nil,
		"-c", "protocol.file.allow=always", "fetch", "--quiet", bundleFile, refspec); err != nil {
		return Verified{}, rules.New(rules.HashMismatch, "the bundle does not contain a usable agent commit")
	}
	commitOut, err := runGit(ctx, gitBin, repoDir, nil, "rev-parse", "refs/agent/incoming")
	if err != nil {
		return Verified{}, err
	}
	commit := strings.TrimSpace(commitOut)
	if !validObjectID(commit) {
		return Verified{}, rules.New(rules.HashMismatch, "the bundle agent ref does not name a commit")
	}

	// Exactly one new commit on top of the served main tip.
	countOut, err := runGit(ctx, gitBin, repoDir, nil, "rev-list", "--count", tip+".."+commit)
	if err != nil {
		return Verified{}, rules.New(rules.HashMismatch, "the agent commit is not part of main's history")
	}
	if strings.TrimSpace(countOut) != "1" {
		return Verified{}, rules.New(rules.HashMismatch, "the bundle must add exactly one commit")
	}

	parentsOut, err := runGit(ctx, gitBin, repoDir, nil, "rev-list", "--parents", "-n", "1", commit)
	if err != nil {
		return Verified{}, err
	}
	parents := strings.Fields(parentsOut)
	if len(parents) != 2 {
		return Verified{}, rules.New(rules.HashMismatch, "the agent commit must have exactly one parent")
	}
	base := parents[1]

	messageOut, err := runGit(ctx, gitBin, repoDir, nil, "show", "-s", "--format=%B", commit)
	if err != nil {
		return Verified{}, err
	}
	meta, ok := commitmeta.Parse(messageOut)
	if !ok {
		return Verified{}, rules.New(rules.HashMismatch, "the commit has no valid Agent-PR-Metadata trailer")
	}
	if err := refname.Validate(meta.Ref); err != nil {
		return Verified{}, rules.New(rules.InvalidRef, "ref must be agent/<sandbox-uid>/<slug>")
	}
	if meta.Repo != req.Repo || meta.Ref != ref {
		return Verified{}, rules.New(rules.HashMismatch, "the commit metadata disagrees with the request or the bundle ref")
	}

	treeOut, err := runGit(ctx, gitBin, repoDir, nil, "rev-parse", commit+"^{tree}")
	if err != nil {
		return Verified{}, err
	}
	tree := strings.TrimSpace(treeOut)

	recomputed, err := canon.Hash(meta.Repo, meta.Ref, commit, tree, meta.Title, meta.Body)
	if err != nil {
		return Verified{}, fmt.Errorf("publish: recompute canonical hash: %w", err)
	}
	if recomputed != req.CanonicalHash {
		return Verified{}, rules.New(rules.HashMismatch, "canonical hash does not match the verified commit")
	}

	files, err := changedFiles(ctx, gitBin, repoDir, base, commit)
	if err != nil {
		return Verified{}, err
	}

	return Verified{
		Repo:          req.Repo,
		Ref:           ref,
		BaseCommit:    base,
		CommitSHA:     commit,
		TreeSHA:       tree,
		Title:         meta.Title,
		Body:          meta.Body,
		Files:         files,
		BundleSHA256:  req.BundleSHA256,
		CanonicalHash: req.CanonicalHash,
	}, nil
}

// changedFiles lists name-status entries between base and commit.
func changedFiles(ctx context.Context, gitBin, repoDir, base, commit string) ([]Change, error) {
	out, err := runGit(ctx, gitBin, repoDir, nil,
		"diff", "--name-status", "--no-renames", "--no-color", base, commit)
	if err != nil {
		return nil, err
	}
	var changes []Change
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 2)
		if len(fields) != 2 {
			return nil, fmt.Errorf("publish: unexpected diff line %q", line)
		}
		changes = append(changes, Change{Status: fields[0], Path: fields[1]})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}

// validObjectID accepts the 40- and 64-character hex object names git may use.
func validObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
