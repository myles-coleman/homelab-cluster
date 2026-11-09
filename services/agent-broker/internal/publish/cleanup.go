// Package publish: branch cleanup (spec 30, FR-38).
package publish

import (
	"context"
	"strings"
	"time"
)

const defaultMaxBranchAge = 72 * time.Hour

// Cleanup closes old app-authored agent PRs and deletes exactly their head refs.
type Cleanup struct {
	// MaxAge is the branch maximum age; zero means 72h.
	MaxAge time.Duration
	// Force cleans fresh app-authored branches too (operator command only).
	Force  bool
	GitHub GitHub
	Now    func() time.Time
}

// CleanupResult records what one cleanup run changed.
type CleanupResult struct {
	ClosedPRs   []int
	DeletedRefs []string
}

// Run performs one cleanup pass for a repository. Only open PRs whose head is in
// refs/heads/agent/ and whose author is this App are eligible; anything else is
// left alone.
func (c *Cleanup) Run(ctx context.Context, repo string) (CleanupResult, error) {
	var result CleanupResult

	login, err := c.GitHub.Login(ctx)
	if err != nil {
		return result, err
	}
	prs, err := c.GitHub.ListOpenAgentPRs(ctx, repo)
	if err != nil {
		return result, err
	}

	maxAge := c.MaxAge
	if maxAge <= 0 {
		maxAge = defaultMaxBranchAge
	}
	now := c.now()

	for _, pr := range prs {
		if !strings.HasPrefix(pr.Head, "refs/heads/agent/") {
			continue
		}
		if pr.Author != login {
			continue
		}
		if !c.Force && now.Sub(pr.CreatedAt) < maxAge {
			continue
		}

		if err := c.GitHub.ClosePR(ctx, repo, pr.Number); err != nil {
			// The branch must never be deleted while its PR is still open.
			return result, err
		}
		if err := c.GitHub.DeleteBranch(ctx, repo, pr.Head); err != nil {
			return result, err
		}
		result.ClosedPRs = append(result.ClosedPRs, pr.Number)
		result.DeletedRefs = append(result.DeletedRefs, pr.Head)
	}
	return result, nil
}

func (c *Cleanup) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
