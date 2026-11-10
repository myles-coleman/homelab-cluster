package publish

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/canon"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/quotas"
)

// eventLog records the observable order of service steps. The order is the
// contract in FR-35/FR-50, so tests assert the sequence, not just the set.
type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (e *eventLog) add(event string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event)
}

func (e *eventLog) snapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.events...)
}

type fakeVerifier struct {
	events   *eventLog
	calls    int
	requests []PublishRequest
	result   Verified
	err      error
}

func (f *fakeVerifier) Verify(_ context.Context, req PublishRequest) (Verified, error) {
	f.calls++
	f.requests = append(f.requests, req)
	f.events.add("verify")
	return f.result, f.err
}

var _ Verifier = (*fakeVerifier)(nil)

type fakePusher struct {
	events    *eventLog
	calls     int
	repo      string
	ref       string
	commitSHA string
	bundle    []byte
	err       error
}

func (f *fakePusher) Push(_ context.Context, repo, ref, commitSHA string, bundle []byte) error {
	f.calls++
	f.repo = repo
	f.ref = ref
	f.commitSHA = commitSHA
	f.bundle = append([]byte(nil), bundle...)
	f.events.add("push")
	return f.err
}

var _ Pusher = (*fakePusher)(nil)

type fakeGitHub struct {
	events *eventLog

	calls int

	openPRs []PullRequest
	listErr error

	findPR  *PullRequest
	findErr error
	findRef string

	createInput DraftPR
	createPR    PullRequest
	createErr   error

	closed   []int
	closeErr error

	deleted   []string
	deleteErr error

	login      string
	loginErr   error
	loginCalls int
}

func (f *fakeGitHub) ListOpenAgentPRs(_ context.Context, _ string) ([]PullRequest, error) {
	f.calls++
	f.events.add("list")
	return append([]PullRequest(nil), f.openPRs...), f.listErr
}

func (f *fakeGitHub) FindOpenPRByHead(_ context.Context, _ string, ref string) (*PullRequest, error) {
	f.calls++
	f.findRef = ref
	f.events.add("find")
	if f.findErr != nil || f.findPR == nil {
		return nil, f.findErr
	}
	pr := *f.findPR
	return &pr, nil
}

func (f *fakeGitHub) CreateDraftPR(_ context.Context, _ string, in DraftPR) (PullRequest, error) {
	f.calls++
	f.createInput = in
	f.events.add("create")
	return f.createPR, f.createErr
}

func (f *fakeGitHub) ClosePR(_ context.Context, _ string, number int) error {
	f.calls++
	f.closed = append(f.closed, number)
	f.events.add("close")
	return f.closeErr
}

func (f *fakeGitHub) DeleteBranch(_ context.Context, _ string, ref string) error {
	f.calls++
	f.deleted = append(f.deleted, ref)
	f.events.add("delete")
	return f.deleteErr
}

func (f *fakeGitHub) Login(_ context.Context) (string, error) {
	f.calls++
	f.loginCalls++
	f.events.add("login")
	if f.login == "" {
		return "agent-broker[bot]", f.loginErr
	}
	return f.login, f.loginErr
}

var _ GitHub = (*fakeGitHub)(nil)

// sampleVerified is a plausible successful verification used by the service and
// handler tests. It deliberately has two changed paths and a real canonical
// hash so the rendered body is not a special case.
func sampleVerified(repo string) Verified {
	ref := "agent/" + testAgentUID + "/" + testAgentSlug
	commit := strings.Repeat("c", 40)
	tree := strings.Repeat("d", 40)
	title := "change docs"
	body := "why"
	hash, err := canon.Hash(repo, ref, commit, tree, title, body)
	if err != nil {
		panic(err)
	}
	return Verified{
		Repo:          repo,
		Ref:           ref,
		BaseCommit:    strings.Repeat("b", 40),
		CommitSHA:     commit,
		TreeSHA:       tree,
		Title:         title,
		Body:          body,
		Files:         []Change{{Status: "M", Path: "docs/a.txt"}, {Status: "A", Path: "docs/c.txt"}},
		BundleSHA256:  strings.Repeat("e", 64),
		CanonicalHash: hash,
	}
}

func newTestService(v Verifier, gh GitHub, p Pusher, lim *quotas.Limiter) *Service {
	return &Service{
		Owner:    "myles-coleman",
		Verifier: v,
		Quotas:   lim,
		GitHub:   gh,
		Pusher:   p,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// allowAllQuotas builds a limiter with wide limits and records the quota check
// in the shared event log. It returns zero open PRs.
func allowAllQuotas(events *eventLog) *quotas.Limiter {
	l := quotas.New(quotas.Limits{
		MaxOpenPRsPerRepo:      100,
		MaxPublishesPerSandbox: 100,
		PublishWindow:          time.Hour,
		MaxBundleBytes:         1 << 20,
		MaxTotalBundleBytes:    1 << 30,
	})
	l.OpenPRs = func(context.Context, string) (int, error) {
		events.add("quota")
		return 0, nil
	}
	return l
}
