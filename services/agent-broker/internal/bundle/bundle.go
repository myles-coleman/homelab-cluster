// Package bundle serves and caches shallow main-tip bundles (spec 30, FR-11,
// FR-12, FR-14).
//
// The validator never speaks to GitHub. It asks the publisher for a repository
// bundle over mTLS and caches the result by (repo, commit) for a bounded TTL.
// Only `nixos` and `homelab-cluster` are addressable.
package bundle

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Repositories the broker is allowed to serve (FR-11).
const (
	RepoNixos          = "nixos"
	RepoHomelabCluster = "homelab-cluster"
)

// maxBundleSize caps how many bytes a publisher response may occupy in memory.
const maxBundleSize = 64 << 20 // 64 MiB

// fetchTimeout bounds one publisher round trip so a stalled peer cannot wedge a
// validator request indefinitely.
const fetchTimeout = 30 * time.Second

var (
	ErrUnknownRepo       = errors.New("bundle: unknown repository")
	ErrCommitUnavailable = errors.New("bundle: requested commit is not the fetched tip")
)

// ValidRepo reports whether repo is one of the two allowed repositories.
func ValidRepo(repo string) bool {
	return repo == RepoNixos || repo == RepoHomelabCluster
}

// Bundle is one fetched repository state.
type Bundle struct {
	Repo    string
	Commit  string
	Content []byte
	Fetched time.Time
}

// Fetcher obtains the current main-tip bundle for a repository.
type Fetcher interface {
	Fetch(ctx context.Context, repo string) (Bundle, error)
}

// Cache wraps a Fetcher with a TTL cache keyed by (repo, commit).
type Cache struct {
	fetcher Fetcher
	ttl     time.Duration
	now     func() time.Time

	mu      sync.Mutex
	entries map[string]Bundle
	latest  map[string]string
}

// NewCache builds a Cache. A non-positive ttl disables retention.
func NewCache(f Fetcher, ttl time.Duration) *Cache {
	return &Cache{
		fetcher: f,
		ttl:     ttl,
		now:     time.Now,
		entries: make(map[string]Bundle),
		latest:  make(map[string]string),
	}
}

// Get returns the cached or freshly fetched tip for repo.
func (c *Cache) Get(ctx context.Context, repo string) (Bundle, error) {
	if !ValidRepo(repo) {
		return Bundle{}, fmt.Errorf("%w: %q", ErrUnknownRepo, repo)
	}

	c.mu.Lock()
	if commit, ok := c.latest[repo]; ok {
		if entry, ok := c.entries[cacheKey(repo, commit)]; ok && c.fresh(entry) {
			c.mu.Unlock()
			return entry, nil
		}
	}
	c.mu.Unlock()

	fetched, err := c.fetcher.Fetch(ctx, repo)
	if err != nil {
		return Bundle{}, err
	}

	c.mu.Lock()
	stored := c.storeLocked(repo, fetched)
	c.mu.Unlock()
	return stored, nil
}

// GetCommit returns the bundle for an exact commit, fetching if necessary. The
// publisher only serves the current tip, so any other commit is unavailable
// (FR-14).
func (c *Cache) GetCommit(ctx context.Context, repo, commit string) (Bundle, error) {
	if !ValidRepo(repo) {
		return Bundle{}, fmt.Errorf("%w: %q", ErrUnknownRepo, repo)
	}

	c.mu.Lock()
	if entry, ok := c.entries[cacheKey(repo, commit)]; ok && c.fresh(entry) {
		c.mu.Unlock()
		return entry, nil
	}
	c.mu.Unlock()

	fetched, err := c.fetcher.Fetch(ctx, repo)
	if err != nil {
		return Bundle{}, err
	}
	if fetched.Commit != commit {
		return Bundle{}, fmt.Errorf("%w: wanted %s, fetched tip %s for %q", ErrCommitUnavailable, commit, fetched.Commit, repo)
	}

	c.mu.Lock()
	stored := c.storeLocked(repo, fetched)
	c.mu.Unlock()
	return stored, nil
}

// storeLocked records a bundle and returns the cached copy. Callers hold c.mu.
func (c *Cache) storeLocked(repo string, b Bundle) Bundle {
	// The TTL starts when the bundle is cached, not when the publisher produced
	// it, so a slow round trip cannot shorten a caller's cache window.
	b.Fetched = c.now()
	// Keep at most one entry per repo. A superseded tip can never be served again
	// (the publisher only exposes the current tip), and retaining every commit
	// ever observed would grow without bound: FR-11 bounds staleness, not
	// retention.
	prefix := repo + "\x00"
	for key := range c.entries {
		if strings.HasPrefix(key, prefix) && key != cacheKey(repo, b.Commit) {
			delete(c.entries, key)
		}
	}
	c.entries[cacheKey(repo, b.Commit)] = b
	c.latest[repo] = b.Commit
	return b
}

// fresh reports whether entry is still inside the TTL. A non-positive TTL means
// nothing is retained.
func (c *Cache) fresh(entry Bundle) bool {
	if c.ttl <= 0 {
		return false
	}
	return c.now().Sub(entry.Fetched) < c.ttl
}

// cacheKey namespaces entries by repository as well as commit, so a commit hash
// shared by two repositories cannot cross-serve content.
func cacheKey(repo, commit string) string {
	return repo + "\x00" + commit
}

// HTTPFetcher fetches bundles from the publisher over mTLS.
type HTTPFetcher struct {
	baseURL    string
	pathPrefix string
	client     *http.Client
}

// NewHTTPFetcher builds a fetcher for baseURL using the supplied client TLS
// configuration. The publisher serves GET /internal/bundle/{repo} and reports the
// commit in the X-Bundle-Commit header.
func NewHTTPFetcher(baseURL string, cfg *tls.Config) *HTTPFetcher {
	return &HTTPFetcher{
		baseURL: baseURL,
		client: &http.Client{
			Timeout:   fetchTimeout,
			Transport: &http.Transport{TLSClientConfig: cfg},
		},
	}
}

// Fetch implements Fetcher.
func (f *HTTPFetcher) Fetch(ctx context.Context, repo string) (Bundle, error) {
	return f.fetchFrom(ctx, f.pathPrefix, repo)
}

// fetchFrom performs one GET against a publisher route prefix.
func (f *HTTPFetcher) fetchFrom(ctx context.Context, prefix, repo string) (Bundle, error) {
	endpoint := strings.TrimRight(f.baseURL, "/") + "/internal/bundle/" + url.PathEscape(repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Bundle{}, fmt.Errorf("bundle: build request for %q: %w", repo, err)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return Bundle{}, fmt.Errorf("bundle: fetch %q: %w", repo, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return Bundle{}, fmt.Errorf("%w: %q", ErrUnknownRepo, repo)
	default:
		// Deliberately exclude the body: it may contain details a caller would
		// log, and the status code is enough to classify the failure.
		return Bundle{}, fmt.Errorf("bundle: publisher returned status %d for %q", resp.StatusCode, repo)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBundleSize+1))
	if err != nil {
		return Bundle{}, fmt.Errorf("bundle: read %q response: %w", repo, err)
	}
	if len(body) > maxBundleSize {
		return Bundle{}, fmt.Errorf("bundle: repository %q exceeds the %d-byte limit", repo, maxBundleSize)
	}

	commit := resp.Header.Get("X-Bundle-Commit")
	if !validCommitSHA(commit) {
		return Bundle{}, errors.New("bundle: publisher returned a missing or malformed commit")
	}

	return Bundle{
		Repo:    repo,
		Commit:  commit,
		Content: body,
		Fetched: time.Now(),
	}, nil
}

// validCommitSHA accepts the 40- or 64-character hex object names git may use.
func validCommitSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}

// HistoryFetcher obtains the full-history bundle used by the validator to apply
// patches (FR-19). It is a separate interface so a caller cannot accidentally
// use the agent-facing bundle for validation.
type HistoryFetcher interface {
	FetchHistory(ctx context.Context, repo string) (commit string, content []byte, err error)
}

// NewHTTPHistoryFetcher points an HTTPFetcher at the publisher's history route.
func NewHTTPHistoryFetcher(baseURL string, cfg *tls.Config) *HTTPFetcher {
	f := NewHTTPFetcher(baseURL, cfg)
	f.pathPrefix = "/internal/history/"
	return f
}

// FetchHistory implements HistoryFetcher over the same wire protocol as Fetch.
func (f *HTTPFetcher) FetchHistory(ctx context.Context, repo string) (string, []byte, error) {
	b, err := f.fetchFrom(ctx, f.pathPrefix, repo)
	if err != nil {
		return "", nil, err
	}
	return b.Commit, b.Content, nil
}
