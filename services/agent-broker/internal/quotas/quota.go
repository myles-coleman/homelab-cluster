// Package quotas enforces the publish limits in spec 30, FR-37.
//
// Limits are deliberately redundant: a per-request byte ceiling stops one huge
// bundle, an open-PR ceiling stops a repository from accumulating branches, and a
// per-sandbox sliding window stops a retry loop from consuming GitHub API calls
// and reviewer attention. Check is a pure read; Record consumes a slot, and the
// service calls it only after a successful publish.
package quotas

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/rules"
)

const (
	defaultMaxOpenPRsPerRepo      = 4
	defaultMaxPublishesPerSandbox = 10
	defaultPublishWindow          = time.Hour
	defaultMaxBundleBytes         = 16 << 20 // 16 MiB
	defaultMaxTotalBundleBytes    = 64 << 20 // 64 MiB
)

// Limits is the safety envelope. A zero value takes the documented default so a
// missing configuration can never mean "unlimited".
type Limits struct {
	MaxOpenPRsPerRepo      int
	MaxPublishesPerSandbox int
	PublishWindow          time.Duration
	MaxBundleBytes         int
	MaxTotalBundleBytes    int
}

// Limiter enforces Limits with an in-memory window.
type Limiter struct {
	Limits Limits
	// OpenPRs is the injected GitHub read. Its error is an infrastructure
	// failure, never a policy denial.
	OpenPRs func(ctx context.Context, repo string) (int, error)
	// Now is injectable for tests; nil means time.Now.
	Now func() time.Time

	mu      sync.Mutex
	records map[string][]record
}

type record struct {
	at    time.Time
	bytes int
}

// New builds a Limiter, filling zero fields with their defaults.
func New(l Limits) *Limiter {
	if l.MaxOpenPRsPerRepo <= 0 {
		l.MaxOpenPRsPerRepo = defaultMaxOpenPRsPerRepo
	}
	if l.MaxPublishesPerSandbox <= 0 {
		l.MaxPublishesPerSandbox = defaultMaxPublishesPerSandbox
	}
	if l.PublishWindow <= 0 {
		l.PublishWindow = defaultPublishWindow
	}
	if l.MaxBundleBytes <= 0 {
		l.MaxBundleBytes = defaultMaxBundleBytes
	}
	if l.MaxTotalBundleBytes <= 0 {
		l.MaxTotalBundleBytes = defaultMaxTotalBundleBytes
	}
	return &Limiter{
		Limits:  l,
		records: make(map[string][]record),
	}
}

// Check rejects a publish that would exceed a limit. Its order is normative: the
// byte ceiling is decided from the request alone, before the injected GitHub
// read, so a cheap rejection never spends an API call.
func (l *Limiter) Check(ctx context.Context, repo, sandboxUID string, bundleBytes int) error {
	if bundleBytes > l.Limits.MaxBundleBytes {
		return rules.New(rules.QuotaExceeded, "bundle exceeds the per-request byte limit")
	}

	if l.OpenPRs != nil {
		open, err := l.OpenPRs(ctx, repo)
		if err != nil {
			return fmt.Errorf("quotas: read open pull requests: %w", err)
		}
		if open >= l.Limits.MaxOpenPRsPerRepo {
			return rules.New(rules.QuotaExceeded, "the repository already has the maximum number of open agent pull requests")
		}
	}

	now := l.now()
	cutoff := now.Add(-l.Limits.PublishWindow)

	l.mu.Lock()
	kept := l.pruneLocked(sandboxUID, cutoff)
	publishes := len(kept)
	totalBytes := 0
	for _, r := range kept {
		totalBytes += r.bytes
	}
	l.mu.Unlock()

	if publishes >= l.Limits.MaxPublishesPerSandbox {
		return rules.New(rules.QuotaExceeded, "the sandbox has reached its publish limit for the window")
	}
	if totalBytes+bundleBytes > l.Limits.MaxTotalBundleBytes {
		return rules.New(rules.QuotaExceeded, "the sandbox has reached its bundle byte budget for the window")
	}
	return nil
}

// Record consumes one publish slot. It must be called only after a successful
// publish.
func (l *Limiter) Record(sandboxUID string, bundleBytes int) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records[sandboxUID] = append(l.records[sandboxUID], record{at: now, bytes: bundleBytes})
}

// pruneLocked drops records that have fallen out of the window. Callers hold mu.
func (l *Limiter) pruneLocked(sandboxUID string, cutoff time.Time) []record {
	kept := l.records[sandboxUID][:0]
	for _, r := range l.records[sandboxUID] {
		if r.at.After(cutoff) {
			kept = append(kept, r)
		}
	}
	if len(kept) == 0 {
		delete(l.records, sandboxUID)
		return nil
	}
	l.records[sandboxUID] = kept
	return kept
}

func (l *Limiter) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}
