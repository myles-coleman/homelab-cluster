package quotas

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/rules"
)

const (
	testRepo      = "nixos"
	testSandboxID = "4a22ef3b-312b-4624-8e64-9a3060579091"
)

func zeroOpenPRs(context.Context, string) (int, error) { return 0, nil }

func wantQuotaRejection(t *testing.T, err error, forbidden ...string) {
	t.Helper()
	rej, ok := rules.From(err)
	if !ok {
		t.Fatalf("error = %v, want a *rules.Rejection", err)
	}
	if rej.RuleID != rules.QuotaExceeded {
		t.Fatalf("rule = %q (%s), want %q", rej.RuleID, rej.Reason, rules.QuotaExceeded)
	}
	if strings.TrimSpace(rej.Reason) == "" {
		t.Error("rejection reason is empty")
	}
	for _, value := range forbidden {
		if value == "" {
			continue
		}
		if strings.Contains(rej.Reason, value) {
			t.Errorf("rejection reason %q echoes caller value %q", rej.Reason, value)
		}
	}
}

// TestDefaultLimits pins the defaults in the design (FR-37). They are part of
// the safety envelope: a zero-valued Limits must not mean "unlimited".
func TestDefaultLimits(t *testing.T) {
	l := New(Limits{})

	if got := l.Limits.MaxOpenPRsPerRepo; got != 4 {
		t.Errorf("MaxOpenPRsPerRepo = %d, want 4", got)
	}
	if got := l.Limits.MaxPublishesPerSandbox; got != 10 {
		t.Errorf("MaxPublishesPerSandbox = %d, want 10", got)
	}
	if got := l.Limits.PublishWindow; got != time.Hour {
		t.Errorf("PublishWindow = %v, want 1h", got)
	}
	if got := l.Limits.MaxBundleBytes; got != 16<<20 {
		t.Errorf("MaxBundleBytes = %d, want 16 MiB", got)
	}
	if got := l.Limits.MaxTotalBundleBytes; got != 64<<20 {
		t.Errorf("MaxTotalBundleBytes = %d, want 64 MiB", got)
	}
}

// TestPerRequestBundleCeiling covers FR-37's per-request byte ceiling. The
// boundary is inclusive: exactly the limit is allowed, one byte over is not.
func TestPerRequestBundleCeiling(t *testing.T) {
	l := New(Limits{})
	l.OpenPRs = zeroOpenPRs

	const limit = 16 << 20
	if err := l.Check(context.Background(), testRepo, testSandboxID, limit); err != nil {
		t.Fatalf("Check(%d) = %v, want nil at the limit", limit, err)
	}
	err := l.Check(context.Background(), testRepo, testSandboxID, limit+1)
	wantQuotaRejection(t, err, testRepo, testSandboxID, "16777217")
}

// TestFifthOpenPRRejects covers FR-37's open-PR ceiling: four open agent PRs
// for a repository means the next publish is the fifth and must be rejected
// before any mutation. The injected read is the only source of that count.
func TestFifthOpenPRRejects(t *testing.T) {
	calls := 0
	l := New(Limits{})
	l.OpenPRs = func(_ context.Context, repo string) (int, error) {
		calls++
		if repo != testRepo {
			t.Errorf("OpenPRs repo = %q, want %q", repo, testRepo)
		}
		return 4, nil
	}
	err := l.Check(context.Background(), testRepo, testSandboxID, 1)
	wantQuotaRejection(t, err, testRepo, testSandboxID)
	if calls != 1 {
		t.Errorf("OpenPRs calls = %d, want 1", calls)
	}

	below := New(Limits{})
	below.OpenPRs = func(context.Context, string) (int, error) { return 3, nil }
	if err := below.Check(context.Background(), testRepo, testSandboxID, 1); err != nil {
		t.Errorf("Check with 3 open PRs = %v, want nil", err)
	}
}

// TestPerSandboxPublishWindow covers FR-37's publishes-per-sandbox window and
// its expiry. The window is an hour by default and Record, not Check, consumes
// the slot.
func TestPerSandboxPublishWindow(t *testing.T) {
	now := time.Date(2026, 11, 9, 1, 0, 0, 0, time.UTC)
	l := New(Limits{})
	l.OpenPRs = zeroOpenPRs
	l.Now = func() time.Time { return now }

	for i := 0; i < 10; i++ {
		if err := l.Check(context.Background(), testRepo, testSandboxID, 1); err != nil {
			t.Fatalf("Check %d of 10 = %v, want nil", i+1, err)
		}
		if i < 9 {
			l.Record(testSandboxID, 1)
		}
	}
	// Nine records and ten checks: Check must not itself consume a slot.
	l.Record(testSandboxID, 1)

	err := l.Check(context.Background(), testRepo, testSandboxID, 1)
	wantQuotaRejection(t, err, testRepo, testSandboxID)

	// One hour after the oldest record the window is empty again.
	now = now.Add(time.Hour + time.Nanosecond)
	if err := l.Check(context.Background(), testRepo, testSandboxID, 1); err != nil {
		t.Errorf("Check after window expiry = %v, want nil", err)
	}
}

// TestTotalBundleWindowCeiling covers FR-37's cumulative byte budget. Bytes
// recorded inside the window count against the next publish in the same
// sandbox; other sandboxes are independent.
func TestTotalBundleWindowCeiling(t *testing.T) {
	l := New(Limits{
		MaxOpenPRsPerRepo:      100,
		MaxPublishesPerSandbox: 100,
		PublishWindow:          time.Hour,
		MaxBundleBytes:         1 << 20,
		MaxTotalBundleBytes:    100,
	})
	l.OpenPRs = zeroOpenPRs

	l.Record(testSandboxID, 60)
	if err := l.Check(context.Background(), testRepo, testSandboxID, 40); err != nil {
		t.Fatalf("Check(40) with 60 recorded = %v, want nil", err)
	}
	err := l.Check(context.Background(), testRepo, testSandboxID, 41)
	wantQuotaRejection(t, err, testRepo, testSandboxID)

	// A different sandbox has its own budget.
	if err := l.Check(context.Background(), testRepo, "11111111-1111-1111-1111-111111111111", 100); err != nil {
		t.Errorf("Check for a different sandbox = %v, want nil", err)
	}
}

// TestCheckDoesNotRecord covers FR-37's "Record is called only after a
// successful publish" wiring. Check is a pure read: repeated successful checks
// must not consume the publish or byte budgets, or a client could exhaust its
// own quota by retrying.
func TestCheckDoesNotRecord(t *testing.T) {
	t.Run("publish count", func(t *testing.T) {
		l := New(Limits{
			MaxOpenPRsPerRepo:      100,
			MaxPublishesPerSandbox: 1,
			PublishWindow:          time.Hour,
			MaxBundleBytes:         1 << 20,
			MaxTotalBundleBytes:    1 << 20,
		})
		l.OpenPRs = zeroOpenPRs

		for i := 0; i < 5; i++ {
			if err := l.Check(context.Background(), testRepo, testSandboxID, 1); err != nil {
				t.Fatalf("Check %d without Record = %v, want nil", i+1, err)
			}
		}
		l.Record(testSandboxID, 1)
		wantQuotaRejection(t, l.Check(context.Background(), testRepo, testSandboxID, 1), testRepo, testSandboxID)
	})

	t.Run("bundle bytes", func(t *testing.T) {
		l := New(Limits{
			MaxOpenPRsPerRepo:      100,
			MaxPublishesPerSandbox: 100,
			PublishWindow:          time.Hour,
			MaxBundleBytes:         1 << 20,
			MaxTotalBundleBytes:    10,
		})
		l.OpenPRs = zeroOpenPRs

		for i := 0; i < 5; i++ {
			if err := l.Check(context.Background(), testRepo, testSandboxID, 10); err != nil {
				t.Fatalf("Check(10) without Record = %v, want nil", err)
			}
		}
		l.Record(testSandboxID, 10)
		wantQuotaRejection(t, l.Check(context.Background(), testRepo, testSandboxID, 1), testRepo, testSandboxID)
	})
}

// TestPerRequestCeilingRunsBeforeOpenPRRead covers FR-37's normative order:
// the byte ceiling is decided from the request alone, before the injected
// GitHub read. A cheap rejection must not spend an API call.
func TestPerRequestCeilingRunsBeforeOpenPRRead(t *testing.T) {
	read := false
	l := New(Limits{MaxOpenPRsPerRepo: 100, MaxBundleBytes: 10, MaxTotalBundleBytes: 100, MaxPublishesPerSandbox: 100})
	l.OpenPRs = func(context.Context, string) (int, error) {
		read = true
		return 0, nil
	}
	err := l.Check(context.Background(), testRepo, testSandboxID, 11)
	wantQuotaRejection(t, err, testRepo, testSandboxID)
	if read {
		t.Error("Check consulted OpenPRs even though the request was already over the byte ceiling")
	}
}

// TestOpenPRReadFailureIsNotAQuotaRejection keeps an infrastructure failure
// from being reported as a policy denial: the caller needs to distinguish "try
// again later" from "you are over quota".
func TestOpenPRReadFailureIsNotAQuotaRejection(t *testing.T) {
	boom := errors.New("github unavailable")
	l := New(Limits{})
	l.OpenPRs = func(context.Context, string) (int, error) { return 0, boom }

	err := l.Check(context.Background(), testRepo, testSandboxID, 1)
	if err == nil {
		t.Fatal("Check returned nil despite the open-PR read failing")
	}
	if _, ok := rules.From(err); ok {
		t.Fatalf("Check turned an infrastructure error into a rejection: %v", err)
	}
	if !errors.Is(err, boom) {
		t.Errorf("error = %v, want it to wrap the injected error", err)
	}
}
