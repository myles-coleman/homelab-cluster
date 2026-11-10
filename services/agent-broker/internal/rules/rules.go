// Package rules holds the stable rejection contract shared by every broker
// component (spec 30, FR-17).
//
// A rejection is a structured error: a rule id that clients can branch on, and a
// one-sentence reason that never echoes rejected content.
package rules

import "errors"

// Stable rule ids. These are API surface: the agent distinguishes "fix the patch"
// from "re-fetch and resubmit" by reading them, so they must not change casually.
const (
	BadRequest         = "bad-request"
	UnknownRepo        = "unknown-repo"
	PathNotAllowed     = "path-not-allowed"
	ForbiddenTreeShape = "forbidden-tree-shape"
	ImportsInjection   = "imports-injection"
	LockfileChange     = "lockfile-change"
	SecretDetected     = "secret-detected"
	StaleBase          = "stale-base"
	InvalidRef         = "invalid-ref"
	PatchApplyFailed   = "patch-apply-failed"
	Unauthenticated    = "unauthenticated"

	// Publish pipeline ids (spec 30, Unit 3). They are API surface too: the
	// validator forwards the publisher's rule id to the sandbox unchanged.
	HashMismatch       = "hash-mismatch"
	QuotaExceeded      = "quota-exceeded"
	BranchExists       = "branch-exists"
	PublishUnavailable = "publish-unavailable"
	PublishFailed      = "publish-failed"
)

// Rejection is the only error type the HTTP layer maps to a 4xx body.
type Rejection struct {
	RuleID string
	Reason string
}

func (r *Rejection) Error() string {
	return r.RuleID + ": " + r.Reason
}

// New builds a rejection error.
func New(ruleID, reason string) error {
	return &Rejection{RuleID: ruleID, Reason: reason}
}

// From extracts a rejection from an error chain.
func From(err error) (*Rejection, bool) {
	var r *Rejection
	if errors.As(err, &r) {
		return r, true
	}
	return nil, false
}
