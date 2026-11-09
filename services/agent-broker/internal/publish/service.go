// Package publish owns the validator-to-GitHub publish pipeline (spec 30,
// FR-33..FR-40, FR-49, FR-50).
//
// The publisher is the only component with a GitHub write credential. It verifies
// the validator's bundle locally, applies the quota envelope, pushes exactly one
// agent branch, and opens a draft pull request. Nothing here may merge, review,
// tag, dispatch a workflow, or mark a PR ready for review.
package publish

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/quotas"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/rules"
)

// PublishRequest is the POST /v1/publish body (FR-33). Bundle is JSON base64.
type PublishRequest struct {
	Repo          string `json:"repo"`
	Bundle        []byte `json:"bundle"`
	BundleSHA256  string `json:"bundle_sha256"`
	CanonicalHash string `json:"canonical_hash"`
}

// Change is one path changed by the agent commit.
type Change struct {
	Status string // A, M, D, T
	Path   string
}

// Verified is a bundle that passed every local check.
type Verified struct {
	Repo          string
	Ref           string // bare agent/<uid>/<slug>
	BaseCommit    string
	CommitSHA     string
	TreeSHA       string
	Title         string
	Body          string
	Files         []Change
	BundleSHA256  string
	CanonicalHash string
}

// Result is one successfully published draft PR.
type Result struct {
	Number    int
	Ref       string // full refs/heads/agent/<uid>/<slug>
	URL       string
	CommitSHA string
}

// Verifier re-derives the canonical hash from the bundle (FR-34).
type Verifier interface {
	Verify(ctx context.Context, req PublishRequest) (Verified, error)
}

// Pusher pushes exactly one commit to exactly one agent ref (FR-35, FR-40).
type Pusher interface {
	Push(ctx context.Context, repo, ref, commitSHA string, bundle []byte) error
}

// GitHub is the narrow REST surface the service needs (FR-39, FR-40).
type GitHub interface {
	ListOpenAgentPRs(ctx context.Context, repo string) ([]PullRequest, error)
	FindOpenPRByHead(ctx context.Context, repo, ref string) (*PullRequest, error)
	CreateDraftPR(ctx context.Context, repo string, in DraftPR) (PullRequest, error)
	ClosePR(ctx context.Context, repo string, number int) error
	DeleteBranch(ctx context.Context, repo, ref string) error
	Login(ctx context.Context) (string, error)
}

// PullRequest mirrors the fields the broker reasons about.
type PullRequest struct {
	Number    int
	Head      string // refs/heads/agent/...
	Author    string
	CreatedAt time.Time
	Draft     bool
}

// DraftPR is a create request. Base is always "main".
type DraftPR struct {
	Title string
	Head  string
	Base  string
	Body  string
}

// Service runs the normative publish sequence.
type Service struct {
	Owner    string
	Verifier Verifier
	Quotas   *quotas.Limiter
	GitHub   GitHub
	Pusher   Pusher
	Log      *slog.Logger
	Now      func() time.Time
}

// Publish verifies, admits, finds an existing PR, pushes, creates the draft PR,
// and finally consumes quota (FR-35, FR-37, FR-50).
func (s *Service) Publish(ctx context.Context, req PublishRequest) (Result, error) {
	verified, err := s.Verifier.Verify(ctx, req)
	if err != nil {
		return Result{}, err
	}

	if s.Quotas != nil {
		if err := s.Quotas.Check(ctx, verified.Repo, sandboxUID(verified.Ref), len(req.Bundle)); err != nil {
			return Result{}, err
		}
	}

	ref := "refs/heads/" + verified.Ref
	existing, err := s.GitHub.FindOpenPRByHead(ctx, verified.Repo, ref)
	if err != nil {
		return Result{}, fmt.Errorf("publish: find open pull request: %w", err)
	}
	if existing != nil {
		return Result{}, rules.New(rules.BranchExists, "an open pull request already uses this ref")
	}

	if err := s.Pusher.Push(ctx, verified.Repo, verified.Ref, verified.CommitSHA, req.Bundle); err != nil {
		return Result{}, fmt.Errorf("publish: push agent branch: %w", err)
	}

	created, err := s.GitHub.CreateDraftPR(ctx, verified.Repo, DraftPR{
		Title: verified.Title,
		Head:  ref,
		Base:  "main",
		Body:  RenderBody(verified),
	})
	if err != nil {
		return Result{}, fmt.Errorf("publish: create draft pull request: %w", err)
	}

	if s.Quotas != nil {
		s.Quotas.Record(sandboxUID(verified.Ref), len(req.Bundle))
	}

	if s.Log != nil {
		s.Log.Info("published draft pull request",
			"repo", verified.Repo,
			"ref", ref,
			"number", created.Number,
			"commit", verified.CommitSHA,
		)
	}

	return Result{
		Number:    created.Number,
		Ref:       ref,
		URL:       fmt.Sprintf("https://github.com/%s/%s/pull/%d", s.Owner, verified.Repo, created.Number),
		CommitSHA: verified.CommitSHA,
	}, nil
}

// sandboxUID extracts the middle component of a bare agent ref. The verifier
// guarantees the shape before the service sees it.
func sandboxUID(ref string) string {
	parts := strings.Split(ref, "/")
	if len(parts) == 3 {
		return parts[1]
	}
	return ""
}
