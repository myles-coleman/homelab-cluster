package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/canon"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/publish"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/rules"
)

type fakePublisher struct {
	calls int
	req   publish.PublishRequest
	err   error
}

func (f *fakePublisher) Publish(_ context.Context, req publish.PublishRequest) error {
	f.calls++
	f.req = req
	return f.err
}

func validPatchBody(t *testing.T, r repoT) map[string]any {
	t.Helper()
	return map[string]any{
		"repo":           "nixos",
		"base_commit":    r.base,
		"title":          "change docs",
		"body":           "why",
		"commit_message": "docs: change",
		"patch":          patchFor(t, r),
	}
}

// TestPatchesCallsPublisherWithReturnedBundle covers FR-33's validator wiring:
// the hook receives the exact bundle the validator built, plus the two hashes
// returned in the 202 body. It runs before the response, so a publisher
// rejection can still change the status.
func TestPatchesCallsPublisherWithReturnedBundle(t *testing.T) {
	r := newRepoT(t)
	s, _ := patchServer(t, r)
	fp := &fakePublisher{}
	s.Publisher = fp

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, liveRequest(t, "/v1/patches", validPatchBody(t, r), true))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if fp.calls != 1 {
		t.Fatalf("publisher calls = %d, want 1", fp.calls)
	}
	if fp.req.Repo != "nixos" {
		t.Errorf("published repo = %q, want nixos", fp.req.Repo)
	}
	if len(fp.req.Bundle) == 0 {
		t.Error("publisher received an empty bundle")
	}
	if fp.req.BundleSHA256 != resp["bundle_sha256"] {
		t.Errorf("bundle_sha256 = %q, want the response value %q", fp.req.BundleSHA256, resp["bundle_sha256"])
	}
	if fp.req.CanonicalHash != resp["canonical_hash"] {
		t.Errorf("canonical_hash = %q, want the response value %q", fp.req.CanonicalHash, resp["canonical_hash"])
	}
	if got := canon.BundleSHA256(fp.req.Bundle); got != fp.req.BundleSHA256 {
		t.Errorf("hash of the published bundle = %q, want %q", got, fp.req.BundleSHA256)
	}
}

// TestPatchesPublisherRejectionMapping covers FR-33's structured rejection
// path: a publisher rejection is surfaced to the sandbox with its rule id and
// the matching 4xx/429, and the audit record is a rejection, not an accept.
func TestPatchesPublisherRejectionMapping(t *testing.T) {
	tests := []struct {
		rule   string
		status int
	}{
		{rule: rules.HashMismatch, status: http.StatusConflict},
		{rule: rules.BranchExists, status: http.StatusConflict},
		{rule: rules.QuotaExceeded, status: http.StatusTooManyRequests},
		{rule: rules.UnknownRepo, status: http.StatusNotFound},
		{rule: rules.InvalidRef, status: http.StatusUnprocessableEntity},
	}

	for _, tc := range tests {
		t.Run(tc.rule, func(t *testing.T) {
			r := newRepoT(t)
			s, auditBuf := patchServer(t, r)
			s.Publisher = &fakePublisher{err: rules.New(tc.rule, "publisher rejected the change")}

			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, liveRequest(t, "/v1/patches", validPatchBody(t, r), true))

			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.status, rec.Body.String())
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			if body["rule_id"] != tc.rule {
				t.Errorf("rule_id = %q, want %q", body["rule_id"], tc.rule)
			}
			if strings.TrimSpace(body["reason"]) == "" {
				t.Error("reason is empty")
			}

			line := strings.TrimSpace(auditBuf.String())
			if !strings.Contains(line, `"decision":"reject"`) {
				t.Errorf("audit record = %s, want decision reject", line)
			}
			if !strings.Contains(line, `"rule_id":"`+tc.rule+`"`) {
				t.Errorf("audit record = %s, want rule_id %s", line, tc.rule)
			}
			if strings.Contains(line, `"decision":"accept"`) {
				t.Errorf("audit record claims accept after a publisher rejection: %s", line)
			}
		})
	}
}

// TestPatchesPublisherOutage covers FR-33's failure mapping: a transport or
// unknown publisher failure becomes 502 publish-unavailable, hides internals,
// and is recorded as that rule.
func TestPatchesPublisherOutage(t *testing.T) {
	r := newRepoT(t)
	s, auditBuf := patchServer(t, r)
	s.Publisher = &fakePublisher{err: errors.New("dial tcp: connection refused")}

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, liveRequest(t, "/v1/patches", validPatchBody(t, r), true))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body %s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body["rule_id"] != rules.PublishUnavailable {
		t.Errorf("rule_id = %q, want %q", body["rule_id"], rules.PublishUnavailable)
	}
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("error body leaked the publisher error: %s", rec.Body.String())
	}

	line := strings.TrimSpace(auditBuf.String())
	if !strings.Contains(line, `"rule_id":"`+rules.PublishUnavailable+`"`) {
		t.Errorf("audit record = %s, want rule_id %s", line, rules.PublishUnavailable)
	}
}

// TestPatchesNilPublisherKeepsUnit2Behaviour covers FR-33's compatibility
// requirement: with no publisher configured, the Unit 2 path still returns the
// exact four-key 202 response.
func TestPatchesNilPublisherKeepsUnit2Behaviour(t *testing.T) {
	r := newRepoT(t)
	s, _ := patchServer(t, r)
	if s.Publisher != nil {
		t.Fatal("test precondition: publisher must be nil")
	}

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, liveRequest(t, "/v1/patches", validPatchBody(t, r), true))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	var keys []string
	for key := range got {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	want := []string{"bundle_sha256", "canonical_hash", "commit_sha", "tree_sha"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Errorf("keys = %v, want exactly %v", keys, want)
	}
}
