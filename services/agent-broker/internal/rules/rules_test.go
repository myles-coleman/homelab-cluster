package rules

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestRuleIDSetIsStable covers FR-17: rule ids are API surface, so a rename or a
// dropped id breaks agents that branch on the value.
func TestRuleIDSetIsStable(t *testing.T) {
	want := []string{
		BadRequest,
		UnknownRepo,
		PathNotAllowed,
		ForbiddenTreeShape,
		ImportsInjection,
		LockfileChange,
		SecretDetected,
		StaleBase,
		InvalidRef,
		PatchApplyFailed,
		Unauthenticated,
	}

	seen := make(map[string]bool, len(want))
	for _, id := range want {
		if id == "" {
			t.Error("a rule id is empty")
			continue
		}
		if seen[id] {
			t.Errorf("duplicate rule id %q", id)
		}
		seen[id] = true
	}

	// The literal values are part of the wire contract, not just the constants.
	expectedLiterals := map[string]string{
		"bad-request":          BadRequest,
		"unknown-repo":         UnknownRepo,
		"path-not-allowed":     PathNotAllowed,
		"forbidden-tree-shape": ForbiddenTreeShape,
		"imports-injection":    ImportsInjection,
		"lockfile-change":      LockfileChange,
		"secret-detected":      SecretDetected,
		"stale-base":           StaleBase,
		"invalid-ref":          InvalidRef,
		"patch-apply-failed":   PatchApplyFailed,
		"unauthenticated":      Unauthenticated,
	}
	for literal, got := range expectedLiterals {
		if got != literal {
			t.Errorf("rule id constant = %q, want %q", got, literal)
		}
	}
}

// TestNewSurvivesFrom covers FR-17's round-trip: the HTTP layer builds a
// rejection and the middleware extracts it unchanged.
func TestNewSurvivesFrom(t *testing.T) {
	const reason = "repository is not managed by this broker"

	err := New(UnknownRepo, reason)
	if err == nil {
		t.Fatal("New returned nil")
	}

	rej, ok := From(err)
	if !ok {
		t.Fatalf("From(New(...)) did not find a rejection in %v", err)
	}
	if rej.RuleID != UnknownRepo {
		t.Errorf("RuleID = %q, want %q", rej.RuleID, UnknownRepo)
	}
	if rej.Reason != reason {
		t.Errorf("Reason = %q, want %q", rej.Reason, reason)
	}

	// Error() is what a human sees in logs; it must name both halves.
	if msg := err.Error(); !strings.Contains(msg, UnknownRepo) || !strings.Contains(msg, reason) {
		t.Errorf("Error() = %q, want it to contain the rule id and reason", msg)
	}
}

// TestFromRejectsUnrelatedErrors makes sure an ordinary error is never mapped to
// a 4xx with a bogus rule id.
func TestFromRejectsUnrelatedErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"nil", nil},
		{"plain error", errors.New("connection reset")},
		{"wrapped plain error", fmt.Errorf("fetch bundle: %w", errors.New("timeout"))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if rej, ok := From(tc.err); ok {
				t.Errorf("From(%v) = (%#v, true), want false", tc.err, rej)
			}
		})
	}
}

// TestFromFindsWrappedRejections covers FR-17's wrapping guarantee: callers add
// context with %w, and the HTTP layer must still recover the rule id.
func TestFromFindsWrappedRejections(t *testing.T) {
	base := New(PathNotAllowed, "path is denied by policy")

	tests := []struct {
		name string
		err  error
	}{
		{"one wrap", fmt.Errorf("check policy: %w", base)},
		{"two wraps", fmt.Errorf("apply patch: %w", fmt.Errorf("check policy: %w", base))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rej, ok := From(tc.err)
			if !ok {
				t.Fatalf("From(%v) did not find the wrapped rejection", tc.err)
			}
			if rej.RuleID != PathNotAllowed {
				t.Errorf("RuleID = %q, want %q", rej.RuleID, PathNotAllowed)
			}
			if rej.Reason != base.(*Rejection).Reason {
				t.Errorf("Reason = %q, want %q", rej.Reason, base.(*Rejection).Reason)
			}
		})
	}

	// errors.Is must not match an unrelated sentinel through a rejection.
	var rej *Rejection
	if !errors.As(base, &rej) {
		t.Error("errors.As did not match the rejection")
	}
}
