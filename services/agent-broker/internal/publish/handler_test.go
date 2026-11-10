package publish

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/audit"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/quotas"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/rules"
)

// publishMux mounts the publish endpoint on the publisher's existing mux. The
// bundle server owns the ValidatorSAN admission logic, so reusing it here keeps
// the 401 cases honest rather than testing a stand-in.
func publishMux(svc *Service) http.Handler {
	return (&BundleServer{
		Owner:   "myles-coleman",
		Tokens:  &stubTokens{},
		Fetcher: &stubBundle{},
		Publish: svc,
	}).Handler()
}

func publishHTTPRequest(t *testing.T, raw []byte, san string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/publish", bytes.NewReader(raw))
	if san != "" {
		u, err := url.Parse(san)
		if err != nil {
			t.Fatalf("parse SAN %q: %v", san, err)
		}
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{URIs: []*url.URL{u}}}}
	}
	return r
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	return body
}

// TestPublishHandlerRequiresValidatorIdentity covers FR-33's admission rule:
// the publish endpoint is reachable only with the validator's client
// certificate, and a rejected request never reaches the service.
func TestPublishHandlerRequiresValidatorIdentity(t *testing.T) {
	tests := map[string]string{
		"no certificate":   "",
		"foreign identity": "spiffe://cowlab.org/ns/agent-sandbox/pod/opencode",
		"other validator":  "spiffe://cowlab.org/ns/agent-validator/service/other",
	}
	for name, san := range tests {
		t.Run(name, func(t *testing.T) {
			gh := &fakeGitHub{}
			pusher := &fakePusher{}
			svc := newTestService(&fakeVerifier{result: sampleVerified("nixos")}, gh, pusher, allowAllQuotas(nil))

			rec := httptest.NewRecorder()
			publishMux(svc).ServeHTTP(rec, publishHTTPRequest(t, []byte(`{}`), san))

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (body %s)", rec.Code, rec.Body.String())
			}
			if got := decodeError(t, rec)["rule_id"]; got != rules.Unauthenticated {
				t.Errorf("rule_id = %q, want %q", got, rules.Unauthenticated)
			}
			if gh.calls != 0 || pusher.calls != 0 {
				t.Errorf("rejected request reached the service: github=%d push=%d", gh.calls, pusher.calls)
			}
		})
	}
}

// TestPublishHandlerCreatedShape covers FR-33/FR-35's 201 body: exactly the
// four contract keys, with the PR number, full ref, and pushed commit.
func TestPublishHandlerCreatedShape(t *testing.T) {
	verified := sampleVerified("nixos")
	gh := &fakeGitHub{
		createPR: PullRequest{Number: 12, Head: "refs/heads/" + verified.Ref, Author: "agent-broker[bot]", Draft: true},
	}
	svc := newTestService(&fakeVerifier{result: verified}, gh, &fakePusher{}, allowAllQuotas(nil))

	raw := mustJSON(t, PublishRequest{
		Repo:          "nixos",
		Bundle:        []byte("bundle"),
		BundleSHA256:  verified.BundleSHA256,
		CanonicalHash: verified.CanonicalHash,
	})
	rec := httptest.NewRecorder()
	publishMux(svc).ServeHTTP(rec, publishHTTPRequest(t, raw, ValidatorSAN))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	var keys []string
	for key := range got {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	wantKeys := []string{"commit_sha", "number", "ref", "url"}
	if strings.Join(keys, ",") != strings.Join(wantKeys, ",") {
		t.Errorf("keys = %v, want exactly %v", keys, wantKeys)
	}
	if got["number"] != float64(12) {
		t.Errorf("number = %v, want 12", got["number"])
	}
	if got["ref"] != "refs/heads/"+verified.Ref {
		t.Errorf("ref = %v, want %q", got["ref"], "refs/heads/"+verified.Ref)
	}
	if got["commit_sha"] != verified.CommitSHA {
		t.Errorf("commit_sha = %v, want %q", got["commit_sha"], verified.CommitSHA)
	}
}

// TestPublishHandlerRuleStatusMapping covers FR-33/FR-34/FR-37's status map.
// A verification failure is a 409 and must not call GitHub; quota is 429.
func TestPublishHandlerRuleStatusMapping(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		rule   string
	}{
		{name: "hash mismatch", err: rules.New(rules.HashMismatch, "hash does not match"), status: http.StatusConflict, rule: rules.HashMismatch},
		{name: "branch exists", err: rules.New(rules.BranchExists, "an open PR already uses the ref"), status: http.StatusConflict, rule: rules.BranchExists},
		{name: "quota exceeded", err: rules.New(rules.QuotaExceeded, "publish quota exceeded"), status: http.StatusTooManyRequests, rule: rules.QuotaExceeded},
		{name: "unknown repo", err: rules.New(rules.UnknownRepo, "repository is not served"), status: http.StatusNotFound, rule: rules.UnknownRepo},
		{name: "invalid ref", err: rules.New(rules.InvalidRef, "ref must be agent/<sandbox-uid>/<slug>"), status: http.StatusUnprocessableEntity, rule: rules.InvalidRef},
		{name: "publish failure", err: errors.New("github returned 500"), status: http.StatusBadGateway, rule: rules.PublishFailed},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gh := &fakeGitHub{}
			pusher := &fakePusher{}
			svc := newTestService(&fakeVerifier{err: tc.err}, gh, pusher, allowAllQuotas(nil))
			raw := mustJSON(t, PublishRequest{Repo: "nixos", Bundle: []byte("bundle")})

			rec := httptest.NewRecorder()
			publishMux(svc).ServeHTTP(rec, publishHTTPRequest(t, raw, ValidatorSAN))

			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.status, rec.Body.String())
			}
			if got := decodeError(t, rec)["rule_id"]; got != tc.rule {
				t.Errorf("rule_id = %q, want %q", got, tc.rule)
			}
			if gh.calls != 0 || pusher.calls != 0 {
				t.Errorf("failed publish mutated state: github=%d push=%d", gh.calls, pusher.calls)
			}
		})
	}
}

// TestPublishHandlerQuotaRejection explicitly covers FR-37's 429: the default
// fourth-open-PR limit rejects the fifth publish at the handler boundary.
func TestPublishHandlerQuotaRejection(t *testing.T) {
	lim := quotas.New(quotas.Limits{})
	lim.OpenPRs = func(context.Context, string) (int, error) { return 4, nil }
	gh := &fakeGitHub{}
	pusher := &fakePusher{}
	svc := newTestService(&fakeVerifier{result: sampleVerified("nixos")}, gh, pusher, lim)

	raw := mustJSON(t, PublishRequest{Repo: "nixos", Bundle: []byte("bundle")})
	rec := httptest.NewRecorder()
	publishMux(svc).ServeHTTP(rec, publishHTTPRequest(t, raw, ValidatorSAN))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (body %s)", rec.Code, rec.Body.String())
	}
	if got := decodeError(t, rec)["rule_id"]; got != rules.QuotaExceeded {
		t.Errorf("rule_id = %q, want %q", got, rules.QuotaExceeded)
	}
	if pusher.calls != 0 || gh.calls != 0 {
		t.Errorf("quota rejection mutated state: github=%d push=%d", gh.calls, pusher.calls)
	}
}

// TestPublishHandlerMalformedBody covers FR-33's request decoding: invalid JSON
// and unknown fields are 400s, not service failures.
func TestPublishHandlerMalformedBody(t *testing.T) {
	tests := map[string]string{
		"not JSON":      "{",
		"unknown field": `{"repo":"nixos","nope":1}`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			gh := &fakeGitHub{}
			pusher := &fakePusher{}
			svc := newTestService(&fakeVerifier{result: sampleVerified("nixos")}, gh, pusher, allowAllQuotas(nil))

			rec := httptest.NewRecorder()
			publishMux(svc).ServeHTTP(rec, publishHTTPRequest(t, []byte(raw), ValidatorSAN))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
			if got := decodeError(t, rec)["rule_id"]; got != rules.BadRequest {
				t.Errorf("rule_id = %q, want %q", got, rules.BadRequest)
			}
			if gh.calls != 0 || pusher.calls != 0 {
				t.Errorf("malformed body reached the service: github=%d push=%d", gh.calls, pusher.calls)
			}
		})
	}
}

// TestPublishAuditWritesOneRejection covers FR-51's publisher half: a rejected
// publish still produces exactly one structured record, carrying the reason and
// the terminal rule id. A missing or duplicated record is a failure.
func TestPublishAuditWritesOneRejection(t *testing.T) {
	var buf bytes.Buffer
	s := &BundleServer{
		Owner:   "myles-coleman",
		Tokens:  &stubTokens{},
		Fetcher: &stubBundle{},
		// Publish is deliberately nil: this is an early rejection, which is the
		// path most likely to skip the audit record.
		Audit: audit.New(&buf),
	}

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, publishHTTPRequest(t, []byte(`{}`), ValidatorSAN))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body %s)", rec.Code, rec.Body.String())
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("audit lines = %d, want 1: %q", len(lines), buf.String())
	}
	var got struct {
		Decision string `json:"decision"`
		RuleID   string `json:"rule_id"`
		Reason   string `json:"reason"`
		Time     string `json:"time"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("decode audit record: %v", err)
	}
	if got.Decision != "reject" || got.RuleID != rules.PublishFailed || got.Reason == "" {
		t.Errorf("audit record = %s", lines[0])
	}
	if got.Time == "" {
		t.Errorf("audit record has no timestamp: %s", lines[0])
	}
}
