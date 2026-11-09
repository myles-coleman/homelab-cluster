package publish

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"
)

// TestCleanupOnlyOldAppAuthoredAgentPRs covers FR-38: cleanup is limited to
// open PRs whose head is in the agent namespace and whose author is this App.
// Fresh branches, operator-authored PRs, and non-agent branches are untouched.
func TestCleanupOnlyOldAppAuthoredAgentPRs(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	const login = "agent-broker[bot]"

	oldAgent := "refs/heads/agent/4a22ef3b-312b-4624-8e64-9a3060579091/old"
	exactAgent := "refs/heads/agent/4a22ef3b-312b-4624-8e64-9a3060579091/exact"
	freshAgent := "refs/heads/agent/4a22ef3b-312b-4624-8e64-9a3060579091/fresh"
	operatorAgent := "refs/heads/agent/4a22ef3b-312b-4624-8e64-9a3060579091/manual"
	humanBranch := "refs/heads/feature/human"

	gh := &fakeGitHub{
		login: login,
		openPRs: []PullRequest{
			{Number: 1, Head: oldAgent, Author: login, CreatedAt: now.Add(-73 * time.Hour), Draft: true},
			{Number: 2, Head: exactAgent, Author: login, CreatedAt: now.Add(-72 * time.Hour), Draft: true},
			{Number: 3, Head: freshAgent, Author: login, CreatedAt: now.Add(-time.Hour), Draft: true},
			{Number: 4, Head: operatorAgent, Author: "myles-coleman", CreatedAt: now.Add(-100 * time.Hour), Draft: true},
			{Number: 5, Head: humanBranch, Author: login, CreatedAt: now.Add(-100 * time.Hour), Draft: true},
		},
	}
	c := &Cleanup{GitHub: gh, Now: func() time.Time { return now }}

	res, err := c.Run(context.Background(), "nixos")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got, want := sortedInts(res.ClosedPRs), []int{1, 2}; !equalInts(got, want) {
		t.Errorf("ClosedPRs = %v, want %v", got, want)
	}
	if got, want := sortedStrings(res.DeletedRefs), []string{exactAgent, oldAgent}; !equalStrings(got, want) {
		t.Errorf("DeletedRefs = %v, want %v", got, want)
	}
	if got, want := sortedInts(gh.closed), []int{1, 2}; !equalInts(got, want) {
		t.Errorf("ClosePR calls = %v, want %v", got, want)
	}
	if got, want := sortedStrings(gh.deleted), []string{exactAgent, oldAgent}; !equalStrings(got, want) {
		t.Errorf("DeleteBranch calls = %v, want %v", got, want)
	}
	if gh.loginCalls != 1 {
		t.Errorf("Login calls = %d, want 1", gh.loginCalls)
	}
	if gh.calls != 1+1+2+2 { // login + list + close/delete for two PRs
		t.Errorf("total GitHub calls = %d, want 6", gh.calls)
	}
}

// TestCleanupForce covers FR-38's operator escape hatch: Force cleans fresh
// app-authored agent PRs too, but never operator-authored ones.
func TestCleanupForce(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	const login = "agent-broker[bot]"
	ref := "refs/heads/agent/4a22ef3b-312b-4624-8e64-9a3060579091/fresh"

	gh := &fakeGitHub{
		login: login,
		openPRs: []PullRequest{
			{Number: 9, Head: ref, Author: login, CreatedAt: now.Add(-time.Minute), Draft: true},
		},
	}
	c := &Cleanup{GitHub: gh, Force: true, Now: func() time.Time { return now }}

	res, err := c.Run(context.Background(), "homelab-cluster")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.ClosedPRs) != 1 || res.ClosedPRs[0] != 9 {
		t.Errorf("ClosedPRs = %v, want [9]", res.ClosedPRs)
	}
	if len(res.DeletedRefs) != 1 || res.DeletedRefs[0] != ref {
		t.Errorf("DeletedRefs = %v, want [%s]", res.DeletedRefs, ref)
	}
}

// TestCleanupHonoursMaxAge covers FR-38's configurable age: MaxAge replaces the
// 72h default rather than adding to it.
func TestCleanupHonoursMaxAge(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	const login = "agent-broker[bot]"
	fresh := "refs/heads/agent/4a22ef3b-312b-4624-8e64-9a3060579091/a"
	old := "refs/heads/agent/4a22ef3b-312b-4624-8e64-9a3060579091/b"

	gh := &fakeGitHub{
		login: login,
		openPRs: []PullRequest{
			{Number: 1, Head: fresh, Author: login, CreatedAt: now.Add(-23 * time.Hour)},
			{Number: 2, Head: old, Author: login, CreatedAt: now.Add(-25 * time.Hour)},
		},
	}
	c := &Cleanup{MaxAge: 24 * time.Hour, GitHub: gh, Now: func() time.Time { return now }}

	res, err := c.Run(context.Background(), "nixos")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.ClosedPRs) != 1 || res.ClosedPRs[0] != 2 {
		t.Errorf("ClosedPRs = %v, want [2]", res.ClosedPRs)
	}
	if len(res.DeletedRefs) != 1 || res.DeletedRefs[0] != old {
		t.Errorf("DeletedRefs = %v, want [%s]", res.DeletedRefs, old)
	}
}

// TestCleanupClosesBeforeDeleting covers FR-38's ordering. A branch must not be
// deleted while its PR is still open; if closing fails, deletion is skipped.
func TestCleanupClosesBeforeDeleting(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	const login = "agent-broker[bot]"
	ref := "refs/heads/agent/4a22ef3b-312b-4624-8e64-9a3060579091/old"

	events := &eventLog{}
	gh := &fakeGitHub{
		events: events,
		login:  login,
		openPRs: []PullRequest{
			{Number: 4, Head: ref, Author: login, CreatedAt: now.Add(-100 * time.Hour)},
		},
	}
	c := &Cleanup{GitHub: gh, Now: func() time.Time { return now }}

	if _, err := c.Run(context.Background(), "nixos"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	order := events.snapshot()
	closeAt, deleteAt := -1, -1
	for i, event := range order {
		switch event {
		case "close":
			closeAt = i
		case "delete":
			deleteAt = i
		}
	}
	if closeAt < 0 || deleteAt < 0 || closeAt > deleteAt {
		t.Errorf("events = %v, want close before delete", order)
	}
}

// TestCleanupSkipsDeleteWhenCloseFails covers FR-38's ordering and failure
// rule: a branch whose PR could not be closed must remain in place, never
// deleted out from under an open PR.
func TestCleanupSkipsDeleteWhenCloseFails(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	const login = "agent-broker[bot]"
	ref := "refs/heads/agent/4a22ef3b-312b-4624-8e64-9a3060579091/old"

	gh := &fakeGitHub{
		login:    login,
		closeErr: errors.New("close failed"),
		openPRs: []PullRequest{
			{Number: 4, Head: ref, Author: login, CreatedAt: now.Add(-100 * time.Hour)},
		},
	}
	c := &Cleanup{GitHub: gh, Now: func() time.Time { return now }}

	if _, err := c.Run(context.Background(), "nixos"); err == nil {
		t.Fatal("Run returned nil despite the close failure")
	}
	if len(gh.deleted) != 0 {
		t.Errorf("DeleteBranch calls = %v, want none after a failed close", gh.deleted)
	}
}

func sortedInts(in []int) []int {
	out := append([]int(nil), in...)
	sort.Ints(out)
	return out
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
