// Package ghttp: the REST client used by the publisher (spec 30, FR-39, FR-40).
//
// Every operation mints exactly one installation token, narrowed to one
// repository and one permission set, and never caches it. The client can list,
// find, create, close and delete branches; it deliberately has no method for
// merging, reviewing, tagging, marking ready for review, or dispatching a
// workflow.
package ghttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/publish"
)

// REST implements publish.GitHub against the GitHub REST API.
type REST struct {
	Owner     string
	LoginRepo string
	Tokens    *AppClient
	BaseURL   string
	HTTP      *http.Client
	Log       *slog.Logger
}

var _ publish.GitHub = (*REST)(nil)

// ghPR mirrors the pull-request fields the broker uses.
type ghPR struct {
	Number int `json:"number"`
	Head   struct {
		Ref string `json:"ref"`
	} `json:"head"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	CreatedAt time.Time `json:"created_at"`
	Draft     bool      `json:"draft"`
}

func (p ghPR) pullRequest() publish.PullRequest {
	return publish.PullRequest{
		Number:    p.Number,
		Head:      p.Head.Ref,
		Author:    p.User.Login,
		CreatedAt: p.CreatedAt,
		Draft:     p.Draft,
	}
}

// ListOpenAgentPRs lists open pull requests and keeps only agent/* heads. It
// follows Link rel="next" so the quota count cannot be truncated by pagination.
func (r *REST) ListOpenAgentPRs(ctx context.Context, repo string) ([]publish.PullRequest, error) {
	token, err := r.token(ctx, repo, map[string]string{"pull_requests": "read"})
	if err != nil {
		return nil, err
	}

	endpoint := r.apiBase() + "/repos/" + url.PathEscape(r.Owner) + "/" + url.PathEscape(repo) +
		"/pulls?state=open&per_page=100"

	var out []publish.PullRequest
	for endpoint != "" {
		resp, err := r.do(ctx, http.MethodGet, endpoint, token, nil)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("ghttp: list pull requests returned status %d", resp.StatusCode)
		}
		var page []ghPR
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&page)
		next := nextLink(resp.Header.Get("Link"))
		resp.Body.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("ghttp: decode pull requests: %w", decodeErr)
		}
		for _, pr := range page {
			if strings.HasPrefix(pr.Head.Ref, "refs/heads/agent/") {
				out = append(out, pr.pullRequest())
			}
		}
		endpoint = next
	}
	return out, nil
}

// FindOpenPRByHead returns the open agent PR for ref, or nil.
func (r *REST) FindOpenPRByHead(ctx context.Context, repo, ref string) (*publish.PullRequest, error) {
	prs, err := r.ListOpenAgentPRs(ctx, repo)
	if err != nil {
		return nil, err
	}
	for i := range prs {
		if prs[i].Head == ref {
			found := prs[i]
			return &found, nil
		}
	}
	return nil, nil
}

// CreateDraftPR opens a draft PR. Head is sent without the refs/heads/ prefix,
// as the REST API expects.
func (r *REST) CreateDraftPR(ctx context.Context, repo string, in publish.DraftPR) (publish.PullRequest, error) {
	token, err := r.token(ctx, repo, map[string]string{"pull_requests": "write"})
	if err != nil {
		return publish.PullRequest{}, err
	}

	payload := struct {
		Title string `json:"title"`
		Head  string `json:"head"`
		Base  string `json:"base"`
		Body  string `json:"body"`
		Draft bool   `json:"draft"`
	}{
		Title: in.Title,
		Head:  strings.TrimPrefix(in.Head, "refs/heads/"),
		Base:  in.Base,
		Body:  in.Body,
		Draft: true,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return publish.PullRequest{}, fmt.Errorf("ghttp: encode pull request: %w", err)
	}

	endpoint := r.apiBase() + "/repos/" + url.PathEscape(r.Owner) + "/" + url.PathEscape(repo) + "/pulls"
	resp, err := r.do(ctx, http.MethodPost, endpoint, token, raw)
	if err != nil {
		return publish.PullRequest{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return publish.PullRequest{}, fmt.Errorf("ghttp: create pull request returned status %d", resp.StatusCode)
	}

	var created ghPR
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&created); err != nil {
		return publish.PullRequest{}, fmt.Errorf("ghttp: decode created pull request: %w", err)
	}
	return created.pullRequest(), nil
}

// ClosePR closes a PR with exactly {"state":"closed"}.
func (r *REST) ClosePR(ctx context.Context, repo string, number int) error {
	token, err := r.token(ctx, repo, map[string]string{"pull_requests": "write"})
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls/%d",
		r.apiBase(), url.PathEscape(r.Owner), url.PathEscape(repo), number)
	resp, err := r.do(ctx, http.MethodPatch, endpoint, token, []byte(`{"state":"closed"}`))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("ghttp: close pull request returned status %d", resp.StatusCode)
	}
	return nil
}

// DeleteBranch deletes exactly one ref under git/refs/heads. Slashes stay path
// separators; each segment is escaped separately.
func (r *REST) DeleteBranch(ctx context.Context, repo, ref string) error {
	branch, err := branchPath(ref)
	if err != nil {
		return err
	}
	token, err := r.token(ctx, repo, map[string]string{"contents": "write"})
	if err != nil {
		return err
	}
	endpoint := r.apiBase() + "/repos/" + url.PathEscape(r.Owner) + "/" + url.PathEscape(repo) +
		"/git/refs/heads/" + branch
	resp, err := r.do(ctx, http.MethodDelete, endpoint, token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ghttp: delete branch returned status %d", resp.StatusCode)
	}
	return nil
}

// Login returns the App installation's login (GET /user).
func (r *REST) Login(ctx context.Context) (string, error) {
	repo := r.LoginRepo
	if repo == "" {
		return "", fmt.Errorf("ghttp: login repository is not configured")
	}
	token, err := r.token(ctx, repo, map[string]string{"pull_requests": "read"})
	if err != nil {
		return "", err
	}
	resp, err := r.do(ctx, http.MethodGet, r.apiBase()+"/user", token, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ghttp: login returned status %d", resp.StatusCode)
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&user); err != nil {
		return "", fmt.Errorf("ghttp: decode login: %w", err)
	}
	if user.Login == "" {
		return "", fmt.Errorf("ghttp: login response has no login")
	}
	return user.Login, nil
}

func (r *REST) token(ctx context.Context, repo string, permissions map[string]string) (string, error) {
	if r.Tokens == nil {
		return "", fmt.Errorf("ghttp: app client is not configured")
	}
	tok, err := r.Tokens.TokenForRepo(ctx, r.Owner, repo, permissions)
	if err != nil {
		return "", err
	}
	return tok.Value, nil
}

func (r *REST) apiBase() string {
	if r.BaseURL != "" {
		return strings.TrimRight(r.BaseURL, "/")
	}
	return defaultAPIBase
}

func (r *REST) do(ctx context.Context, method, endpoint, token string, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, fmt.Errorf("ghttp: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := r.HTTP
	if client == nil {
		client = &http.Client{Timeout: apiTimeout}
	}
	if r.Log != nil {
		// The URL and method are safe; the token never reaches a log line.
		r.Log.Debug("github request", "method", method, "url", endpoint)
	}
	return client.Do(req)
}

// nextLink extracts the rel="next" URL from an RFC 8288 Link header.
func nextLink(header string) string {
	for _, part := range strings.Split(header, ",") {
		if !strings.Contains(part, `rel="next"`) {
			continue
		}
		start := strings.Index(part, "<")
		end := strings.Index(part, ">")
		if start >= 0 && end > start {
			return part[start+1 : end]
		}
	}
	return ""
}

// branchPath strips refs/heads/ and escapes every remaining segment separately.
func branchPath(ref string) (string, error) {
	const prefix = "refs/heads/"
	if !strings.HasPrefix(ref, prefix) {
		return "", fmt.Errorf("ghttp: ref is not under refs/heads")
	}
	parts := strings.Split(strings.TrimPrefix(ref, prefix), "/")
	if len(parts) == 0 {
		return "", fmt.Errorf("ghttp: ref has no branch path")
	}
	escaped := make([]string, len(parts))
	for i, part := range parts {
		if part == "" {
			return "", fmt.Errorf("ghttp: ref has an empty component")
		}
		escaped[i] = url.PathEscape(part)
	}
	return strings.Join(escaped, "/"), nil
}
