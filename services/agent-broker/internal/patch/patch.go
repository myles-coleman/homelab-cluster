// Package patch turns a submitted patch into a verified commit (spec 30, Unit 2).
//
// The pipeline for one request:
//
//  1. fetch the full-history bundle from the publisher and materialise a scratch
//     repository (the validator never trusts the agent's tree);
//  2. reject unless base_commit is an ancestor of the served main tip (FR-18);
//  3. check out base_commit into a clean worktree;
//  4. apply the patch with hooks, filters and attribute overrides disabled, and
//     fail closed on any apply warning (FR-19);
//  5. enforce the tree-shape rules (FR-22), the allowlist (FR-20/21), the imports
//     rule (FR-23), lock-file rejection (FR-24) and the scanner (FR-25/26);
//  6. create exactly one commit on top of base with a committer identity derived
//     from the Sandbox UID (FR-27);
//  7. emit a bundle of exactly that commit and compute bundle_sha256 (FR-28).
//
// Every rejection is a *rules.Rejection; no partial artifact survives one.
package patch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/bundle"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/commitmeta"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/policy"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/refname"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/rules"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/scan"
)

const (
	// maxPatchSize bounds one submission; the handler applies the same cap before
	// decoding so a huge body cannot be buffered twice.
	maxPatchSize = 1 << 20
	// maxScannedFile bounds how much of one file the scanner sees.
	maxScannedFile = 1 << 20
	// maxHistoryBundle bounds the fetched history.
	maxHistoryBundle = 512 << 20
)

// importsRe matches an imports declaration anywhere in the added text, even when
// the `=` sits on a later line (FR-23). Matching anywhere is deliberate: a
// one-line `{ imports = [ ./x ]; }` declares an import just as much as a
// top-level declaration, and anchoring at line start would let it through.
var importsRe = regexp.MustCompile(`\bimports[ \t\r\n]*=`)

// validCommitID accepts the 40- and 64-character hex object names git may use.
func validCommitID(s string) bool {
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

// Request is one submission.
type Request struct {
	Repo          string
	Ref           string
	BaseCommit    string
	Title         string
	Body          string
	CommitMessage string
	Patch         []byte
	SandboxUID    string
}

// Result is a verified commit.
type Result struct {
	CommitSHA string
	TreeSHA   string
	// Files are the repository-relative paths the patch changed.
	Files []string
	// Bundle is a thin git bundle containing the new commit with base as its
	// prerequisite (FR-28).
	Bundle []byte
}

// Applier owns the scratch work and the policy evaluation.
type Applier struct {
	GitBin  string
	WorkDir string
	History bundle.HistoryFetcher
	Policy  *policy.Policy
	Scanner *scan.Scanner
	Now     func() time.Time
}

// Apply runs the pipeline. It returns a Result or a *rules.Rejection.
func (a *Applier) Apply(ctx context.Context, req Request) (Result, error) {
	if a.History == nil {
		return Result{}, errors.New("patch: history fetcher is not configured")
	}
	if a.Policy == nil {
		return Result{}, errors.New("patch: policy is not configured")
	}
	scanner := a.Scanner
	if scanner == nil {
		scanner = scan.New()
	}
	gitBin := a.GitBin
	if gitBin == "" {
		gitBin = "git"
	}
	workDir := a.WorkDir
	if workDir == "" {
		workDir = os.TempDir()
	}
	if !bundle.ValidRepo(req.Repo) {
		return Result{}, rules.New(rules.UnknownRepo, "repository is not served")
	}
	if !validCommitID(req.BaseCommit) {
		return Result{}, rules.New(rules.BadRequest, "base_commit is not a commit id")
	}
	if len(req.Patch) == 0 || len(req.Patch) > maxPatchSize {
		return Result{}, rules.New(rules.BadRequest, "patch is empty or over the size limit")
	}
	if err := refname.Validate(req.Ref); err != nil {
		return Result{}, rules.New(rules.InvalidRef, "ref must be agent/<sandbox-uid>/<slug>")
	}

	tip, history, err := a.History.FetchHistory(ctx, req.Repo)
	if err != nil {
		return Result{}, fmt.Errorf("patch: fetch history for %q: %w", req.Repo, err)
	}
	if len(history) == 0 || len(history) > maxHistoryBundle {
		return Result{}, fmt.Errorf("patch: history bundle for %q has an unusable size", req.Repo)
	}

	scratch, err := os.MkdirTemp(workDir, "patch-*")
	if err != nil {
		return Result{}, fmt.Errorf("patch: create scratch directory: %w", err)
	}
	defer os.RemoveAll(scratch)

	repoDir := filepath.Join(scratch, "repo")
	historyFile := filepath.Join(scratch, "history.bundle")
	if err := os.WriteFile(historyFile, history, 0o600); err != nil {
		return Result{}, fmt.Errorf("patch: write history bundle: %w", err)
	}

	if _, err := runGit(ctx, gitBin, scratch, nil, "init", "--quiet", repoDir); err != nil {
		return Result{}, err
	}
	// Fetching from a bundle file is a local transport; allow it explicitly.
	if _, err := runGit(ctx, gitBin, repoDir, nil,
		"-c", "protocol.file.allow=always", "fetch", "--quiet", historyFile, "HEAD"); err != nil {
		return Result{}, err
	}
	tipOut, err := runGit(ctx, gitBin, repoDir, nil, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return Result{}, err
	}
	fetchedTip := strings.TrimSpace(tipOut)
	if fetchedTip != tip {
		return Result{}, fmt.Errorf("patch: publisher reported tip %s but the bundle carries %s", tip, fetchedTip)
	}

	// A commit id that does not exist in the fetched history can never be an
	// ancestor. Check it explicitly: merge-base exits 128 (not 1) for an unknown
	// name, which would otherwise surface as an internal error instead of the
	// actionable stale-base rejection.
	if _, err := runGit(ctx, gitBin, repoDir, nil, "cat-file", "-e", req.BaseCommit+"^{commit}"); err != nil {
		return Result{}, rules.New(rules.StaleBase,
			fmt.Sprintf("base_commit is not present in main; the current tip is %s", fetchedTip))
	}

	// FR-18: the patch must apply on top of a commit that still exists in main.
	if _, err := runGit(ctx, gitBin, repoDir, nil, "merge-base", "--is-ancestor", req.BaseCommit, fetchedTip); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return Result{}, rules.New(rules.StaleBase,
				fmt.Sprintf("base_commit is not an ancestor of main; the current tip is %s", fetchedTip))
		}
		return Result{}, err
	}

	if _, err := runGit(ctx, gitBin, repoDir, nil, "checkout", "--quiet", "--detach", req.BaseCommit); err != nil {
		return Result{}, err
	}

	// FR-19: hardened apply. Attributes from the global file are disabled, hooks
	// are disabled, whitespace errors are fatal, and any warning on stderr is
	// treated as a failure (fail closed).
	applyArgs := []string{
		"-c", "core.attributesFile=/dev/null",
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.autocrlf=false",
		"-c", "core.safecrlf=false",
		"apply", "--index", "--binary", "--whitespace=error", "-",
	}
	if _, applyErr := runGitInput(ctx, gitBin, repoDir, nil, req.Patch, applyArgs...); applyErr != nil {
		return Result{}, rules.New(rules.PatchApplyFailed, "patch does not apply cleanly to base_commit")
	}

	changes, err := stagedChanges(ctx, gitBin, repoDir)
	if err != nil {
		return Result{}, err
	}
	if len(changes) == 0 {
		return Result{}, rules.New(rules.BadRequest, "patch changes nothing")
	}

	var changedPaths []string
	for _, c := range changes {
		if err := enforceChange(req.Repo, c, a.Policy); err != nil {
			return Result{}, err
		}
		changedPaths = append(changedPaths, c.Path)
	}

	if err := enforceImports(ctx, gitBin, repoDir, changes); err != nil {
		return Result{}, err
	}
	if err := enforceScan(ctx, gitBin, repoDir, changes, req, scanner); err != nil {
		return Result{}, err
	}

	// FR-27: exactly one commit, no signature, committer derived from the UID.
	//
	// The timestamp comes from base_commit, not the wall clock. commit_sha is part
	// of canonical_hash, and FR-29 requires identical inputs to produce an
	// identical hash: a wall-clock timestamp would make every resubmission hash
	// differently even though nothing about the change changed.
	identity := strings.ToLower(req.SandboxUID)
	if identity == "" {
		return Result{}, rules.New(rules.BadRequest, "sandbox identity is missing")
	}
	dateOut, err := runGit(ctx, gitBin, repoDir, nil, "show", "-s", "--format=%ct", req.BaseCommit)
	if err != nil {
		return Result{}, err
	}
	baseSeconds, err := strconv.ParseInt(strings.TrimSpace(dateOut), 10, 64)
	if err != nil {
		return Result{}, fmt.Errorf("patch: base commit has an unusable timestamp: %w", err)
	}
	commitDate := time.Unix(baseSeconds, 0).UTC().Format(time.RFC3339)
	env := []string{
		"GIT_AUTHOR_NAME=agent-sandbox",
		"GIT_AUTHOR_EMAIL=" + identity + "@sandbox.invalid",
		"GIT_COMMITTER_NAME=agent-sandbox",
		"GIT_COMMITTER_EMAIL=" + identity + "@sandbox.invalid",
		"GIT_AUTHOR_DATE=" + commitDate,
		"GIT_COMMITTER_DATE=" + commitDate,
	}
	// FR-34: the publisher only receives hashes, so the commit message carries the
	// metadata the canonical hash needs (repo, ref, title, body). It is appended
	// last so Parse's last-line rule makes it authoritative.
	trailer, err := commitmeta.Encode(commitmeta.Metadata{
		Repo:  req.Repo,
		Ref:   req.Ref,
		Title: req.Title,
		Body:  req.Body,
	})
	if err != nil {
		return Result{}, fmt.Errorf("patch: encode commit metadata: %w", err)
	}
	message := strings.TrimRight(req.CommitMessage, "\n") + "\n\n" + trailer

	if _, err := runGitEnv(ctx, gitBin, repoDir, env,
		"-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null",
		"commit", "--quiet", "-m", message); err != nil {
		return Result{}, err
	}

	countOut, err := runGit(ctx, gitBin, repoDir, nil, "rev-list", "--count", req.BaseCommit+"..HEAD")
	if err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(countOut) != "1" {
		return Result{}, fmt.Errorf("patch: expected exactly one new commit, got %s", strings.TrimSpace(countOut))
	}
	parentsOut, err := runGit(ctx, gitBin, repoDir, nil, "rev-list", "--parents", "-n", "1", "HEAD")
	if err != nil {
		return Result{}, err
	}
	if fields := strings.Fields(parentsOut); len(fields) != 2 {
		return Result{}, errors.New("patch: created commit is not a single-parent commit")
	}

	commitSHA, err := runGit(ctx, gitBin, repoDir, nil, "rev-parse", "HEAD")
	if err != nil {
		return Result{}, err
	}
	treeSHA, err := runGit(ctx, gitBin, repoDir, nil, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return Result{}, err
	}

	// FR-28/FR-34: a thin bundle of exactly the new commit, with base as
	// prerequisite, exposed under the agent ref the publisher will read and
	// HEAD for the existing bundle/tooling contract.
	agentRef := "refs/heads/" + req.Ref
	if _, err := runGit(ctx, gitBin, repoDir, nil, "update-ref", agentRef, "HEAD"); err != nil {
		return Result{}, err
	}
	bundleFile := filepath.Join(scratch, "out.bundle")
	if _, err := runGit(ctx, gitBin, repoDir, nil,
		"bundle", "create", bundleFile, agentRef, "HEAD", "^"+req.BaseCommit); err != nil {
		return Result{}, err
	}
	content, err := os.ReadFile(bundleFile)
	if err != nil {
		return Result{}, fmt.Errorf("patch: read bundle: %w", err)
	}

	sort.Strings(changedPaths)
	return Result{
		CommitSHA: strings.TrimSpace(commitSHA),
		TreeSHA:   strings.TrimSpace(treeSHA),
		Files:     changedPaths,
		Bundle:    content,
	}, nil
}

// change is one staged path.
type change struct {
	OldMode string
	NewMode string
	Status  string // A, M, D, T
	Path    string
}

// stagedChanges parses `git diff --cached --raw -z --no-renames`. Renames are
// split into delete+add so every affected path is evaluated individually.
func stagedChanges(ctx context.Context, gitBin, repoDir string) ([]change, error) {
	out, err := runGit(ctx, gitBin, repoDir, nil,
		"diff", "--cached", "--raw", "-z", "--no-renames", "--no-color")
	if err != nil {
		return nil, err
	}
	fields := strings.Split(out, "\x00")
	var changes []change
	for i := 0; i+1 < len(fields); i += 2 {
		meta := strings.TrimPrefix(fields[i], ":")
		if meta == "" {
			continue
		}
		parts := strings.Fields(meta)
		if len(parts) != 5 {
			return nil, fmt.Errorf("patch: unexpected diff metadata %q", fields[i])
		}
		changes = append(changes, change{
			OldMode: parts[0],
			NewMode: parts[1],
			Status:  parts[4],
			Path:    fields[i+1],
		})
	}
	return changes, nil
}

// enforceChange applies the per-path rules: tree shape (FR-22), allowlist
// (FR-20/21) and lock files (FR-24).
func enforceChange(repo string, c change, p *policy.Policy) error {
	if c.Path == "" || strings.HasPrefix(c.Path, "/") || c.Path == ".." ||
		strings.HasPrefix(c.Path, "../") || strings.Contains(c.Path, "/../") {
		return rules.New(rules.ForbiddenTreeShape, "path escapes the repository root")
	}
	switch c.NewMode {
	case "000000": // deletion
	case "100644": // regular file
	default:
		return rules.New(rules.ForbiddenTreeShape,
			fmt.Sprintf("unsupported file mode %s for %s", c.NewMode, c.Path))
	}
	if c.Status == "T" {
		return rules.New(rules.ForbiddenTreeShape, "type changes are not permitted: "+c.Path)
	}
	if path.Base(c.Path) == ".gitattributes" {
		return rules.New(rules.ForbiddenTreeShape, ".gitattributes may not be changed: "+c.Path)
	}
	base := path.Base(c.Path)
	if base == "flake.lock" || strings.HasSuffix(c.Path, ".terraform.lock.hcl") {
		return rules.New(rules.LockfileChange, "lock files may not be changed by the agent: "+c.Path)
	}
	if err := p.Check(repo, c.Path); err != nil {
		if rej, ok := rules.From(err); ok {
			rej.Reason = rej.Reason + ": " + c.Path
		}
		return err
	}
	return nil
}

// enforceImports rejects any added line that declares imports in a .nix file
// (FR-23). Context lines are ignored, so existing imports are not a problem.
func enforceImports(ctx context.Context, gitBin, repoDir string, changes []change) error {
	for _, c := range changes {
		if c.Status == "D" || !strings.HasSuffix(c.Path, ".nix") {
			continue
		}
		diff, err := runGit(ctx, gitBin, repoDir, nil,
			"diff", "--cached", "-U0", "--no-color", "--", c.Path)
		if err != nil {
			return err
		}
		var added strings.Builder
		for _, line := range strings.Split(diff, "\n") {
			if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
				added.WriteString(strings.TrimPrefix(line, "+"))
				added.WriteString("\n")
			}
		}
		if importsRe.MatchString(added.String()) {
			return rules.New(rules.ImportsInjection, "an imports declaration may not be added: "+c.Path)
		}
	}
	return nil
}

// enforceScan runs the scanner over every added or modified file plus the
// metadata fields (FR-25). It reports the location, never the content.
func enforceScan(ctx context.Context, gitBin, repoDir string, changes []change, req Request, scanner *scan.Scanner) error {
	for _, c := range changes {
		if c.Status == "D" {
			continue
		}
		content, err := runGitBytes(ctx, gitBin, repoDir, nil, "show", ":"+c.Path)
		if err != nil {
			return err
		}
		if len(content) > maxScannedFile {
			content = content[:maxScannedFile]
		}
		if findings := scanner.Text(c.Path, content); len(findings) > 0 {
			return rules.New(rules.SecretDetected, "secret-shaped content at "+findings[0].Location)
		}
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"title", req.Title},
		{"body", req.Body},
		{"commit_message", req.CommitMessage},
	} {
		if findings := scanner.Text(field.name, []byte(field.value)); len(findings) > 0 {
			return rules.New(rules.SecretDetected, "secret-shaped content in "+field.name)
		}
	}
	return nil
}

// runGit runs git and fails on a non-zero exit or any stderr output. Warnings are
// failures by design (FR-19 applies to apply; the same strictness is safe for the
// rest of the pipeline).
func runGit(ctx context.Context, gitBin, dir string, env []string, args ...string) (string, error) {
	out, err := runGitBytes(ctx, gitBin, dir, env, args...)
	return string(out), err
}

func runGitBytes(ctx context.Context, gitBin, dir string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, gitBin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("patch: git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		return nil, fmt.Errorf("patch: git %s wrote to stderr: %s", strings.Join(args, " "), msg)
	}
	return stdout.Bytes(), nil
}

// runGitInput is runGit with stdin. Used for `git apply -`.
func runGitInput(ctx context.Context, gitBin, dir string, env []string, stdin []byte, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, gitBin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("patch: git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		return "", fmt.Errorf("patch: git %s wrote to stderr: %s", strings.Join(args, " "), msg)
	}
	return stdout.String(), nil
}

// runGitEnv is runGit with extra environment variables (commit identity).
func runGitEnv(ctx context.Context, gitBin, dir string, env []string, args ...string) (string, error) {
	return runGit(ctx, gitBin, dir, env, args...)
}

var ErrNotImplemented = errors.New("patch: not implemented")
