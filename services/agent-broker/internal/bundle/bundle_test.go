package bundle

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestValidRepo pins the addressable set to exactly the two repositories in
// FR-11; the publisher must not become an arbitrary git proxy.
func TestValidRepo(t *testing.T) {
	tests := []struct {
		repo string
		want bool
	}{
		{repo: RepoNixos, want: true},
		{repo: RepoHomelabCluster, want: true},
		{repo: "", want: false},
		{repo: "nixos.git", want: false},
		{repo: "Nixos", want: false},
		{repo: "homelab", want: false},
		{repo: "nixos ", want: false},
		{repo: "dotfiles", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.repo, func(t *testing.T) {
			if got := ValidRepo(tc.repo); got != tc.want {
				t.Errorf("ValidRepo(%q) = %v, want %v", tc.repo, got, tc.want)
			}
		})
	}
}

// TestCacheGetServesTipWithinTTL checks that repeated reads in one TTL window
// hit the fetcher once. Every extra fetch is another request against GitHub.
func TestCacheGetServesTipWithinTTL(t *testing.T) {
	clock := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	fetcher := &countingFetcher{bundle: Bundle{Commit: "abc123", Content: []byte("tree")}}
	cache := NewCache(fetcher, time.Minute)
	cache.now = func() time.Time { return clock }

	first, err := cache.Get(context.Background(), RepoNixos)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	second, err := cache.Get(context.Background(), RepoNixos)
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}

	if got := fetcher.callCount(); got != 1 {
		t.Errorf("fetcher calls = %d, want 1 (second read should be served from cache)", got)
	}
	if first.Commit != "abc123" || second.Commit != "abc123" {
		t.Errorf("commits = %q, %q, want abc123 from both reads", first.Commit, second.Commit)
	}
	if string(second.Content) != "tree" {
		t.Errorf("cached content = %q, want %q", second.Content, "tree")
	}
}

// TestCacheGetRefetchesAfterTTL makes sure a cached tip cannot outlive its TTL;
// the validator must eventually observe a new main tip.
func TestCacheGetRefetchesAfterTTL(t *testing.T) {
	clock := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	fetcher := &countingFetcher{bundle: Bundle{Commit: "abc123", Content: []byte("old")}}
	cache := NewCache(fetcher, 30*time.Second)
	cache.now = func() time.Time { return clock }

	if _, err := cache.Get(context.Background(), RepoNixos); err != nil {
		t.Fatalf("first Get: %v", err)
	}

	fetcher.setBundle(Bundle{Commit: "def456", Content: []byte("new")})
	clock = clock.Add(31 * time.Second)

	got, err := cache.Get(context.Background(), RepoNixos)
	if err != nil {
		t.Fatalf("Get after TTL: %v", err)
	}
	if got.Commit != "def456" {
		t.Errorf("commit after TTL = %q, want def456", got.Commit)
	}
	if calls := fetcher.callCount(); calls != 2 {
		t.Errorf("fetcher calls = %d, want 2 (expired entry must be refetched)", calls)
	}
}

// TestCacheGetSurfacesFetchErrorAndCachesNothing guarantees a failed fetch is
// not mistaken for an empty bundle and is retried on the next read.
func TestCacheGetSurfacesFetchErrorAndCachesNothing(t *testing.T) {
	wantErr := errors.New("publisher unreachable")
	fetcher := &countingFetcher{err: wantErr}
	cache := NewCache(fetcher, time.Minute)

	for attempt := 1; attempt <= 2; attempt++ {
		got, err := cache.Get(context.Background(), RepoNixos)
		if !errors.Is(err, wantErr) {
			t.Fatalf("attempt %d error = %v, want %v", attempt, err, wantErr)
		}
		if got.Commit != "" || got.Content != nil {
			t.Errorf("attempt %d returned a bundle %+v alongside an error", attempt, got)
		}
	}

	if calls := fetcher.callCount(); calls != 2 {
		t.Errorf("fetcher calls = %d, want 2 (a failure must not be cached)", calls)
	}
}

// TestCacheGetRejectsUnknownRepo is the cache's own allow-list check; it must
// not even reach the fetcher for a repository outside FR-11.
func TestCacheGetRejectsUnknownRepo(t *testing.T) {
	fetcher := &countingFetcher{bundle: Bundle{Commit: "abc123"}}
	cache := NewCache(fetcher, time.Minute)

	if _, err := cache.Get(context.Background(), "dotfiles"); !errors.Is(err, ErrUnknownRepo) {
		t.Fatalf("Get(unknown) error = %v, want %v", err, ErrUnknownRepo)
	}
	if calls := fetcher.callCount(); calls != 0 {
		t.Errorf("fetcher calls = %d, want 0 for an unknown repository", calls)
	}
}

// TestCacheGetCommit checks the two FR-14 branches: an already-cached commit is
// served without a fetch, and any other commit is refused because the publisher
// only ever serves the current tip.
func TestCacheGetCommit(t *testing.T) {
	clock := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	fetcher := &countingFetcher{bundle: Bundle{Commit: "abc123", Content: []byte("tree")}}
	cache := NewCache(fetcher, time.Hour)
	cache.now = func() time.Time { return clock }

	if _, err := cache.Get(context.Background(), RepoNixos); err != nil {
		t.Fatalf("warm Get: %v", err)
	}

	got, err := cache.GetCommit(context.Background(), RepoNixos, "abc123")
	if err != nil {
		t.Fatalf("GetCommit(cached): %v", err)
	}
	if got.Commit != "abc123" || string(got.Content) != "tree" {
		t.Errorf("cached commit = %+v, want commit abc123 with content %q", got, "tree")
	}
	if calls := fetcher.callCount(); calls != 1 {
		t.Errorf("fetcher calls = %d, want 1 (cached commit must not refetch)", calls)
	}

	if _, err := cache.GetCommit(context.Background(), RepoNixos, "def456"); !errors.Is(err, ErrCommitUnavailable) {
		t.Fatalf("GetCommit(non-tip) error = %v, want %v", err, ErrCommitUnavailable)
	}
}

// TestHTTPFetcherFetchesBundle pins the wire protocol in FR-14: GET
// /internal/bundle/<repo>, commit in X-Bundle-Commit, body untouched.
func TestHTTPFetcherFetchesBundle(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"

	var mu sync.Mutex
	var gotMethod, gotPath string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotMethod, gotPath = r.Method, r.URL.Path
		mu.Unlock()
		w.Header().Set("X-Bundle-Commit", commit)
		_, _ = w.Write([]byte("bundle-bytes"))
	}))
	defer server.Close()

	// InsecureSkipVerify is confined to this test: the server certificate is
	// generated by httptest and there is no CA to hand to the fetcher.
	fetcher := NewHTTPFetcher(server.URL, &tls.Config{InsecureSkipVerify: true})

	got, err := fetcher.Fetch(context.Background(), RepoNixos)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	mu.Lock()
	method, path := gotMethod, gotPath
	mu.Unlock()
	if method != http.MethodGet {
		t.Errorf("method = %q, want GET", method)
	}
	if path != "/internal/bundle/"+RepoNixos {
		t.Errorf("path = %q, want %q", path, "/internal/bundle/"+RepoNixos)
	}
	if got.Repo != RepoNixos {
		t.Errorf("bundle repo = %q, want %q", got.Repo, RepoNixos)
	}
	if got.Commit != commit {
		t.Errorf("bundle commit = %q, want %q (from X-Bundle-Commit)", got.Commit, commit)
	}
	if string(got.Content) != "bundle-bytes" {
		t.Errorf("bundle content = %q, want %q", got.Content, "bundle-bytes")
	}
}

// TestHTTPFetcherRejectsMalformedCommit keeps a missing or non-SHA header out of
// the cache: an empty commit would become a valid-looking cache key.
func TestHTTPFetcherRejectsMalformedCommit(t *testing.T) {
	for name, header := range map[string]string{
		"missing":   "",
		"non-hex":   "not-a-sha",
		"too short": "abc123",
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if header != "" {
					w.Header().Set("X-Bundle-Commit", header)
				}
				_, _ = w.Write([]byte("bundle-bytes"))
			}))
			defer server.Close()

			fetcher := NewHTTPFetcher(server.URL, &tls.Config{InsecureSkipVerify: true})
			if _, err := fetcher.Fetch(context.Background(), RepoNixos); err == nil {
				t.Fatalf("Fetch accepted X-Bundle-Commit %q", header)
			}
		})
	}
}

// TestCacheKeepsOneEntryPerRepo bounds retention: FR-11 bounds staleness, but a
// cache that keeps every commit ever fetched grows without limit.
func TestCacheKeepsOneEntryPerRepo(t *testing.T) {
	clock := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	fetcher := &countingFetcher{bundle: Bundle{Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	cache := NewCache(fetcher, time.Minute)
	cache.now = func() time.Time { return clock }

	if _, err := cache.Get(context.Background(), RepoNixos); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	clock = clock.Add(2 * time.Minute)
	fetcher.setBundle(Bundle{Commit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"})
	if _, err := cache.Get(context.Background(), RepoNixos); err != nil {
		t.Fatalf("second Get: %v", err)
	}

	cache.mu.Lock()
	entries := len(cache.entries)
	cache.mu.Unlock()
	if entries != 1 {
		t.Errorf("cache holds %d entries for one repo, want 1", entries)
	}
}

// TestHTTPFetcherMapsNotFound keeps an unknown repository a typed rejection
// rather than a generic transport error the caller might retry.
func TestHTTPFetcherMapsNotFound(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no such repository", http.StatusNotFound)
	}))
	defer server.Close()

	fetcher := NewHTTPFetcher(server.URL, &tls.Config{InsecureSkipVerify: true})

	if _, err := fetcher.Fetch(context.Background(), RepoNixos); !errors.Is(err, ErrUnknownRepo) {
		t.Fatalf("Fetch(404) error = %v, want %v", err, ErrUnknownRepo)
	}
}

type countingFetcher struct {
	mu     sync.Mutex
	calls  int
	bundle Bundle
	err    error
}

func (f *countingFetcher) Fetch(_ context.Context, repo string) (Bundle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++
	if f.err != nil {
		return Bundle{}, f.err
	}
	out := f.bundle
	out.Repo = repo
	return out, nil
}

func (f *countingFetcher) setBundle(b Bundle) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bundle = b
}

func (f *countingFetcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}
