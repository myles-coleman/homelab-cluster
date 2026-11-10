// Package audit writes one structured record per request (spec 30, FR-31, FR-51).
//
// A record is NDJSON on stdout for `kubectl logs`, contains no secret material,
// and is written exactly once per request: accepted or rejected, never both.
// The same redacted record is appended to the local store and, when configured,
// exported to the OTLP collector (FR-52).
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
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
	Reason        string    `json:"reason"`
	RuleOutcomes  []string  `json:"rule_outcomes"`
	DurationMS    int64     `json:"duration_ms"`
	Time          time.Time `json:"time"`
}

// Exporter ships one already-redacted record to a remote collector. A failure
// is reported to the caller but never fails the local audit write (FR-52).
type Exporter interface {
	Export(ctx context.Context, r Record) error
}

// Logger writes records as one JSON object per line, mirrors them into the
// optional local store, and exports them to the optional collector.
type Logger struct {
	Out      io.Writer
	Now      func() time.Time
	Store    *Store
	Exporter Exporter
}

// New builds a Logger over out.
func New(out io.Writer) *Logger {
	return &Logger{Out: out, Now: time.Now}
}

// Write appends one record.
func (l *Logger) Write(r Record) error {
	return l.WriteContext(context.Background(), r)
}

// WriteContext appends one record, honouring ctx for the export call.
func (l *Logger) WriteContext(ctx context.Context, r Record) error {
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

	// Redact before the record reaches any echo path: stdout, store and OTLP
	// all see the same sanitised copy (FR-51).
	r = RedactRecord(r)

	// Encode emits a single compact object plus a newline, satisfying NDJSON
	// without ever pretty-printing (FR-31).
	enc := json.NewEncoder(l.Out)
	if err := enc.Encode(r); err != nil {
		return err
	}

	if l.Store != nil {
		if err := l.Store.Append(r); err != nil {
			return err
		}
	}

	// The collector is best-effort: its outage never costs the local trail.
	if l.Exporter != nil {
		_ = l.Exporter.Export(ctx, r)
	}
	return nil
}

// RedactRecord applies Redact to every string field.
func RedactRecord(r Record) Record {
	r.RequestID = Redact(r.RequestID)
	r.SandboxUID = Redact(r.SandboxUID)
	r.Repo = Redact(r.Repo)
	r.Ref = Redact(r.Ref)
	r.BaseCommit = Redact(r.BaseCommit)
	r.CommitSHA = Redact(r.CommitSHA)
	r.TreeSHA = Redact(r.TreeSHA)
	r.BundleSHA256 = Redact(r.BundleSHA256)
	r.CanonicalHash = Redact(r.CanonicalHash)
	r.Decision = Redact(r.Decision)
	r.RuleID = Redact(r.RuleID)
	r.Reason = Redact(r.Reason)
	if r.RuleOutcomes != nil {
		outcomes := make([]string, len(r.RuleOutcomes))
		for i, outcome := range r.RuleOutcomes {
			outcomes[i] = Redact(outcome)
		}
		r.RuleOutcomes = outcomes
	}
	return r
}

// redactionMark replaces every recognised secret-shaped substring.
const redactionMark = "[REDACTED]"

// redactors are the secret shapes FR-51 names. Order matters: the PEM block is
// matched first so its body cannot be reinterpreted, then the short token
// shapes. Each whole match becomes the redaction mark.
var redactors = []*regexp.Regexp{
	// A complete PEM private-key block, algorithm-qualified or not.
	regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|$)`),
	// GitHub personal access, OAuth, user, server and refresh tokens.
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{30,}`),
	// AWS access key ids.
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	// JSON Web Tokens: header.payload.signature.
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`),
	// OpenSSH authorized_keys entries.
	regexp.MustCompile(`ssh-(?:ed25519|rsa|dss)[ \t]+AAAA[A-Za-z0-9+/=]+(?:[ \t]+\S+)?`),
	// Credential assignments. The whole assignment is replaced so no part of
	// the value survives; prose such as "token bucket" has no operator and is
	// left untouched.
	regexp.MustCompile(`(?i)\b(?:password|passwd|secret|token|api[_-]?key)\s*[:=]\s*["']?[^\s"']+["']?`),
}

// Redact replaces secret-shaped substrings in text with [REDACTED]. UUIDs,
// git SHAs and ordinary prose are deliberately left alone.
func Redact(text string) string {
	if text == "" {
		return ""
	}
	for _, re := range redactors {
		text = re.ReplaceAllString(text, redactionMark)
	}
	return text
}
