// Package audit writes one structured record per request (spec 30, FR-31, FR-51).
//
// A record is NDJSON on stdout for `kubectl logs`, contains no secret material,
// and is written exactly once per request: accepted or rejected, never both.
package audit

import (
	"encoding/json"
	"errors"
	"io"
	"time"
)

// Decision values.
const (
	Accept = "accept"
	Reject = "reject"
)

// Record is the audit contract. Field names are part of the log schema.
type Record struct {
	RequestID     string    `json:"request_id"`
	SandboxUID    string    `json:"sandbox_uid"`
	Repo          string    `json:"repo"`
	Ref           string    `json:"ref"`
	BaseCommit    string    `json:"base_commit"`
	CommitSHA     string    `json:"commit_sha"`
	TreeSHA       string    `json:"tree_sha"`
	BundleSHA256  string    `json:"bundle_sha256"`
	CanonicalHash string    `json:"canonical_hash"`
	Decision      string    `json:"decision"`
	RuleID        string    `json:"rule_id"`
	DurationMS    int64     `json:"duration_ms"`
	Time          time.Time `json:"time"`
}

// Logger writes records as one JSON object per line.
type Logger struct {
	Out io.Writer
	Now func() time.Time
}

// New builds a Logger over out.
func New(out io.Writer) *Logger {
	return &Logger{Out: out, Now: time.Now}
}

// Write appends one record.
func (l *Logger) Write(r Record) error {
	if l == nil || l.Out == nil {
		return errors.New("audit: nil writer")
	}

	now := l.Now
	if now == nil {
		now = time.Now
	}
	// The logger owns the timestamp, in UTC with nanosecond precision, so every
	// consumer sees the same schema regardless of the caller (FR-31).
	r.Time = now().UTC()

	// Encode emits a single compact object plus a newline, satisfying NDJSON
	// without ever pretty-printing (FR-31).
	enc := json.NewEncoder(l.Out)
	return enc.Encode(r)
}
