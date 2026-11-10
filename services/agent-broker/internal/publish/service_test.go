package publish

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/quotas"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/rules"
)

// TestPublishOrder covers FR-35/FR-50's normative sequence: verification and
// quota admission happen before any mutating GitHub call, and the push happens
// before the draft PR is created.
func TestPublishOrder(t *testing.T) {
	events := &eventLog{}
	verified := sampleVerified("nixos")
	gh := &fakeGitHub{
		events:   events,
		createPR: PullRequest{Number: 12, Head: "refs/heads/" + verified.Ref, Author: "agent-broker[bot]", Draft: true},
	}
	pusher := &fakePusher{events: events}
	svc := newTestService(&fakeVerifier{events: events, result: verified}, gh, pusher, allowAllQuotas(events))

	res, err := svc.Publish(context.Background(), PublishRequest{
		Repo:          "nixos",
		Bundle:        []byte("bundle"),
		BundleSHA256:  verified.BundleSHA256,
		CanonicalHash: verified.CanonicalHash,
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	wantOrder := []string{"verify", "quota", "find", "push", "create"}
	gotOrder := events.snapshot()
	if strings.Join(gotOrder, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("order = %v, want %v", gotOrder, wantOrder)
	}

	if pusher.repo != "nixos" || pusher.ref != verified.Ref || pusher.commitSHA != verified.CommitSHA {
		t.Errorf("Push(%q, %q, %q), want (nixos, %q, %q)", pusher.repo, pusher.ref, pusher.commitSHA, verified.Ref, verified.CommitSHA)
	}
	if string(pusher.bundle) != "bundle" {
		t.Errorf("Push bundle = %q, want the request bundle", pusher.bundle)
	}

	if gh.findRef != "refs/heads/"+verified.Ref {
		t.Errorf("FindOpenPRByHead ref = %q, want %q", gh.findRef, "refs/heads/"+verified.Ref)
	}
	if gh.createInput.Base != "main" {
		t.Errorf("CreateDraftPR Base = %q, want main", gh.createInput.Base)
	}
	if gh.createInput.Head != "refs/heads/"+verified.Ref {
		t.Errorf("CreateDraftPR Head = %q, want %q", gh.createInput.Head, "refs/heads/"+verified.Ref)
	}
	if gh.createInput.Title != verified.Title {
		t.Errorf("CreateDraftPR Title = %q, want %q", gh.createInput.Title, verified.Title)
	}
	if gh.createInput.Body != RenderBody(verified) {
		t.Errorf("CreateDraftPR body is not RenderBody(Verified)")
	}

	if res.Number != 12 {
		t.Errorf("Result.Number = %d, want 12", res.Number)
	}
	if res.Ref != "refs/heads/"+verified.Ref {
		t.Errorf("Result.Ref = %q, want %q", res.Ref, "refs/heads/"+verified.Ref)
	}
	if res.CommitSHA != verified.CommitSHA {
		t.Errorf("Result.CommitSHA = %q, want %q", res.CommitSHA, verified.CommitSHA)
	}
}

// TestPublishHashMismatchMakesNoGitHubCalls covers FR-34: verification failure
// stops the pipeline before quota, push, or any GitHub operation.
func TestPublishHashMismatchMakesNoGitHubCalls(t *testing.T) {
	events := &eventLog{}
	gh := &fakeGitHub{events: events}
	pusher := &fakePusher{events: events}
	svc := newTestService(&fakeVerifier{
		events: events,
		err:    rules.New(rules.HashMismatch, "bundle bytes do not match bundle_sha256"),
	}, gh, pusher, allowAllQuotas(events))

	_, err := svc.Publish(context.Background(), PublishRequest{Repo: "nixos", Bundle: []byte("bundle")})
	wantRejection(t, err, rules.HashMismatch)

	if gh.calls != 0 {
		t.Errorf("GitHub calls = %d, want 0", gh.calls)
	}
	if pusher.calls != 0 {
		t.Errorf("Push calls = %d, want 0", pusher.calls)
	}
	if got := events.snapshot(); len(got) != 1 || got[0] != "verify" {
		t.Errorf("events = %v, want only the verify step", got)
	}
	if len(gh.closed) != 0 || len(gh.deleted) != 0 {
		t.Errorf("cleanup-style mutations ran during publish: closed=%v deleted=%v", gh.closed, gh.deleted)
	}
}

// TestPublishExistingPRIsBranchExists covers FR-35: a PR already open for the
// same agent ref is a conflict, and the service must not push over it.
func TestPublishExistingPRIsBranchExists(t *testing.T) {
	events := &eventLog{}
	verified := sampleVerified("nixos")
	gh := &fakeGitHub{
		events: events,
		findPR: &PullRequest{Number: 7, Head: "refs/heads/" + verified.Ref, Author: "agent-broker[bot]", Draft: true},
	}
	pusher := &fakePusher{events: events}
	svc := newTestService(&fakeVerifier{events: events, result: verified}, gh, pusher, allowAllQuotas(events))

	_, err := svc.Publish(context.Background(), PublishRequest{Repo: "nixos", Bundle: []byte("bundle")})
	wantRejection(t, err, rules.BranchExists)

	if pusher.calls != 0 {
		t.Errorf("Push calls = %d, want 0 when the branch is already published", pusher.calls)
	}
	if len(gh.createInput.Title) != 0 {
		t.Errorf("CreateDraftPR was called with %+v, want no create", gh.createInput)
	}
}

// TestPublishQuotaRejection covers FR-37: the limiter is consulted before the
// branch lookup, and a quota rejection performs no mutation.
func TestPublishQuotaRejection(t *testing.T) {
	events := &eventLog{}
	verified := sampleVerified("nixos")
	gh := &fakeGitHub{events: events}
	pusher := &fakePusher{events: events}
	lim := quotas.New(quotas.Limits{})
	lim.OpenPRs = func(context.Context, string) (int, error) { return 4, nil }

	svc := newTestService(&fakeVerifier{events: events, result: verified}, gh, pusher, lim)

	_, err := svc.Publish(context.Background(), PublishRequest{Repo: "nixos", Bundle: []byte("bundle")})
	wantRejection(t, err, rules.QuotaExceeded)

	if gh.calls != 0 {
		t.Errorf("GitHub calls = %d, want 0", gh.calls)
	}
	if pusher.calls != 0 {
		t.Errorf("Push calls = %d, want 0", pusher.calls)
	}
}

// TestRenderBody pins FR-50's body contract: section order, the canonical hash,
// the changed-path bullets, and the exact repo-specific blast-radius sentence.
func TestRenderBody(t *testing.T) {
	tests := []struct {
		repo  string
		blast string
	}{
		{
			repo:  "homelab-cluster",
			blast: "merging triggers ArgoCD reconciliation of the cluster, including `homelab`, which grants effective root on the host that runs the sandbox node.",
		},
		{
			repo:  "nixos",
			blast: "merging triggers the deploy-rs deployment for `homelab` and `bee-gpu-server`.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.repo, func(t *testing.T) {
			v := sampleVerified(tc.repo)
			body := RenderBody(v)

			sections := []string{"## Canonical hash", "## Validation", "## Changed paths", "## Blast radius"}
			last := -1
			for _, section := range sections {
				idx := strings.Index(body, section)
				if idx < 0 {
					t.Fatalf("body is missing section %q:\n%s", section, body)
				}
				if idx <= last {
					t.Fatalf("section %q is out of order in:\n%s", section, body)
				}
				last = idx
			}

			if !strings.Contains(body, "`"+v.CanonicalHash+"`") {
				t.Errorf("body does not contain the canonical hash in backticks:\n%s", body)
			}
			if !strings.Contains(body, "bundle_sha256") {
				t.Errorf("body does not mention bundle_sha256 verification:\n%s", body)
			}
			if !strings.Contains(body, "re-derived") {
				t.Errorf("body does not mention publisher re-derivation:\n%s", body)
			}
			if !strings.Contains(body, "single non-merge commit") {
				t.Errorf("body does not mention the single non-merge commit:\n%s", body)
			}
			if !strings.Contains(body, v.BaseCommit) {
				t.Errorf("body does not name base commit %s:\n%s", v.BaseCommit, body)
			}
			if want := fmt.Sprintf("%d changed paths", len(v.Files)); !strings.Contains(body, want) {
				t.Errorf("body does not contain %q:\n%s", want, body)
			}
			for _, change := range v.Files {
				if !strings.Contains(body, change.Status+" "+change.Path) {
					t.Errorf("body is missing changed path %q %q:\n%s", change.Status, change.Path, body)
				}
			}
			if !strings.Contains(body, tc.blast) {
				t.Errorf("body is missing the exact blast-radius sentence:\nwant: %s\ngot:\n%s", tc.blast, body)
			}
		})
	}
}

// TestPublishRecordsOnlyAfterSuccess covers FR-37's wiring: Record runs after
// the draft PR is created, so a rejected publish must not consume a sandbox's
// publish window.
func TestPublishRecordsOnlyAfterSuccess(t *testing.T) {
	t.Run("success consumes a slot", func(t *testing.T) {
		events := &eventLog{}
		verified := sampleVerified("nixos")
		gh := &fakeGitHub{events: events, createPR: PullRequest{Number: 1, Head: "refs/heads/" + verified.Ref}}
		pusher := &fakePusher{events: events}
		lim := quotas.New(quotas.Limits{
			MaxOpenPRsPerRepo:      100,
			MaxPublishesPerSandbox: 1,
			PublishWindow:          time.Hour,
			MaxBundleBytes:         1 << 20,
			MaxTotalBundleBytes:    1 << 20,
		})
		lim.OpenPRs = func(context.Context, string) (int, error) { return 0, nil }
		svc := newTestService(&fakeVerifier{events: events, result: verified}, gh, pusher, lim)
		req := PublishRequest{Repo: "nixos", Bundle: []byte("bundle")}

		if _, err := svc.Publish(context.Background(), req); err != nil {
			t.Fatalf("first Publish: %v", err)
		}
		_, err := svc.Publish(context.Background(), req)
		wantRejection(t, err, rules.QuotaExceeded)
	})

	t.Run("rejection does not consume a slot", func(t *testing.T) {
		events := &eventLog{}
		verified := sampleVerified("nixos")
		gh := &fakeGitHub{
			events: events,
			findPR: &PullRequest{Number: 3, Head: "refs/heads/" + verified.Ref},
		}
		pusher := &fakePusher{events: events}
		lim := quotas.New(quotas.Limits{
			MaxOpenPRsPerRepo:      100,
			MaxPublishesPerSandbox: 1,
			PublishWindow:          time.Hour,
			MaxBundleBytes:         1 << 20,
			MaxTotalBundleBytes:    1 << 20,
		})
		lim.OpenPRs = func(context.Context, string) (int, error) { return 0, nil }
		svc := newTestService(&fakeVerifier{events: events, result: verified}, gh, pusher, lim)
		req := PublishRequest{Repo: "nixos", Bundle: []byte("bundle")}

		if _, err := svc.Publish(context.Background(), req); err == nil {
			t.Fatal("expected branch-exists rejection")
		} else {
			wantRejection(t, err, rules.BranchExists)
		}

		gh.findPR = nil
		if _, err := svc.Publish(context.Background(), req); err != nil {
			t.Fatalf("Publish after a rejected attempt: %v (the rejection consumed quota)", err)
		}
	})
}

// TestPublishDeletesBranchWhenCreateFails covers FR-35/FR-38: if the draft PR
// cannot be created after the branch was pushed, the publisher rolls the branch
// back before returning. The full refs/heads/... form is what DeleteBranch
// receives; a dangling bare branch would block every retry with branch-exists.
func TestPublishDeletesBranchWhenCreateFails(t *testing.T) {
	events := &eventLog{}
	verified := sampleVerified("nixos")
	gh := &fakeGitHub{
		events:    events,
		createErr: rules.New(rules.PublishUnavailable, "github create failed"),
	}
	pusher := &fakePusher{events: events}
	svc := newTestService(&fakeVerifier{events: events, result: verified}, gh, pusher, allowAllQuotas(events))

	_, err := svc.Publish(context.Background(), PublishRequest{Repo: "nixos", Bundle: []byte("bundle")})
	if err == nil {
		t.Fatal("Publish returned nil, want the create-draft-PR error")
	}

	wantRef := "refs/heads/" + verified.Ref
	if len(gh.deleted) != 1 || gh.deleted[0] != wantRef {
		t.Fatalf("deleted refs = %v, want exactly [%s] (the just-pushed branch must be rolled back)", gh.deleted, wantRef)
	}

	wantOrder := []string{"verify", "quota", "find", "push", "create", "delete"}
	if got := events.snapshot(); strings.Join(got, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("order = %v, want %v (rollback must happen after create and before returning)", got, wantOrder)
	}
}

// TestPublishRejectsOversizeBundleBeforeVerify covers FR-37: the per-request
// byte ceiling is decided from the request alone, before any verification work
// or GitHub call. A huge bundle must be the cheapest possible rejection.
func TestPublishRejectsOversizeBundleBeforeVerify(t *testing.T) {
	events := &eventLog{}
	verified := sampleVerified("nixos")
	verifier := &fakeVerifier{events: events, result: verified}
	gh := &fakeGitHub{events: events}
	pusher := &fakePusher{events: events}
	lim := quotas.New(quotas.Limits{
		MaxOpenPRsPerRepo:      100,
		MaxPublishesPerSandbox: 100,
		PublishWindow:          time.Hour,
		MaxBundleBytes:         8,
		MaxTotalBundleBytes:    1 << 20,
	})
	lim.OpenPRs = func(context.Context, string) (int, error) { return 0, nil }
	svc := newTestService(verifier, gh, pusher, lim)

	_, err := svc.Publish(context.Background(), PublishRequest{Repo: "nixos", Bundle: []byte("0123456789")})
	wantRejection(t, err, rules.QuotaExceeded)

	if verifier.calls != 0 {
		t.Errorf("verifier calls = %d, want 0 (the size ceiling must be decided before verification)", verifier.calls)
	}
	if gh.calls != 0 {
		t.Errorf("GitHub calls = %d, want 0", gh.calls)
	}
	if pusher.calls != 0 {
		t.Errorf("Push calls = %d, want 0", pusher.calls)
	}
}

// slowVerifier makes the check/record window observable: without service-level
// serialization two concurrent publishes both verify, both see an empty quota
// window, and both succeed.
type slowVerifier struct {
	delay  time.Duration
	result Verified

	mu    sync.Mutex
	calls int
}

func (v *slowVerifier) Verify(ctx context.Context, _ PublishRequest) (Verified, error) {
	v.mu.Lock()
	v.calls++
	v.mu.Unlock()

	select {
	case <-time.After(v.delay):
		return v.result, nil
	case <-ctx.Done():
		return Verified{}, ctx.Err()
	}
}

var _ Verifier = (*slowVerifier)(nil)

// statelessGitHub avoids shared writes in the concurrency test so a missing
// service lock shows up as two successes, not as a data race in test plumbing.
type statelessGitHub struct{}

func (statelessGitHub) ListOpenAgentPRs(context.Context, string) ([]PullRequest, error) {
	return nil, nil
}

func (statelessGitHub) FindOpenPRByHead(context.Context, string, string) (*PullRequest, error) {
	return nil, nil
}

func (statelessGitHub) CreateDraftPR(_ context.Context, _ string, in DraftPR) (PullRequest, error) {
	return PullRequest{Number: 1, Head: in.Head, Author: "agent-broker[bot]", Draft: true}, nil
}

func (statelessGitHub) ClosePR(context.Context, string, int) error { return nil }

func (statelessGitHub) DeleteBranch(context.Context, string, string) error { return nil }

func (statelessGitHub) Login(context.Context) (string, error) { return "agent-broker[bot]", nil }

var _ GitHub = statelessGitHub{}

type statelessPusher struct {
	delay time.Duration
}

func (p statelessPusher) Push(ctx context.Context, _ string, _ string, _ string, _ []byte) error {
	if p.delay > 0 {
		select {
		case <-time.After(p.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

var _ Pusher = statelessPusher{}

// TestPublishSerializesConcurrentPublishes covers FR-37 against one replica:
// two publishes for the same sandbox start together, and the service must
// serialize the quota check and record so exactly one succeeds. The slow
// verifier widens the check/record window that a naive implementation leaves
// open. Run this under -race in the gate.
func TestPublishSerializesConcurrentPublishes(t *testing.T) {
	verified := sampleVerified("nixos")
	verifier := &slowVerifier{delay: 20 * time.Millisecond, result: verified}
	lim := quotas.New(quotas.Limits{
		MaxPublishesPerSandbox: 1,
		MaxBundleBytes:         1 << 20,
		MaxTotalBundleBytes:    1 << 20,
	})
	// A slow verifier makes the two verifications overlap; a slow push then
	// holds both publishers inside the check->record window long enough that a
	// missing service lock shows up as two successes deterministically.
	svc := newTestService(verifier, statelessGitHub{}, statelessPusher{delay: 50 * time.Millisecond}, lim)
	req := PublishRequest{Repo: "nixos", Bundle: []byte("bundle")}

	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.Publish(context.Background(), req)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	successes, quotaRejections := 0, 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		rej, ok := rules.From(err)
		if !ok {
			t.Fatalf("error = %v, want a *rules.Rejection", err)
		}
		if rej.RuleID != rules.QuotaExceeded {
			t.Fatalf("rule id = %q (%s), want %q", rej.RuleID, rej.Reason, rules.QuotaExceeded)
		}
		quotaRejections++
	}
	if successes != 1 || quotaRejections != 1 {
		t.Fatalf("successes = %d, quota rejections = %d, want exactly one of each (publishes must be serialized)", successes, quotaRejections)
	}
}
