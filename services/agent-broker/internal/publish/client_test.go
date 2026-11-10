package publish

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/rules"
)

// newPublisherClient wires the validator's real publish client to a TLS test
// server. httptest.NewServer.Client() trusts the test certificate, so the
// request path (including TLS) is exercised without weakening the client.
func newPublisherClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	return &Client{BaseURL: srv.URL, HTTP: srv.Client()}
}

// TestClientPublishCreated covers the validator's success path: a 2xx from the
// publisher is a successful publish (FR-33).
func TestClientPublishCreated(t *testing.T) {
	c := newPublisherClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		w.WriteHeader(http.StatusCreated)
	})

	if err := c.Publish(context.Background(), PublishRequest{Repo: "nixos", Bundle: []byte("bundle")}); err != nil {
		t.Fatalf("Publish: %v, want nil for 201", err)
	}
}

// TestClientPublishRejection covers FR-33/FR-34: the publisher's structured
// rejection is forwarded as a *rules.Rejection with the same id and reason, not
// flattened into an opaque network error.
func TestClientPublishRejection(t *testing.T) {
	const reason = "bundle bytes do not match bundle_sha256"
	c := newPublisherClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"rule_id":"hash-mismatch","reason":"`+reason+`"}`)
	})

	err := c.Publish(context.Background(), PublishRequest{Repo: "nixos", Bundle: []byte("bundle")})
	var rej *rules.Rejection
	if !errors.As(err, &rej) {
		t.Fatalf("error = %v, want a *rules.Rejection", err)
	}
	if rej.RuleID != rules.HashMismatch {
		t.Errorf("rule id = %q, want %q", rej.RuleID, rules.HashMismatch)
	}
	if rej.Reason != reason {
		t.Errorf("reason = %q, want %q", rej.Reason, reason)
	}
}

// TestClientPublishUnparseableFailure covers the outage path: a non-2xx
// response without a structured rejection must surface as publish-unavailable,
// never as a success or a raw decode error.
func TestClientPublishUnparseableFailure(t *testing.T) {
	c := newPublisherClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `<html>internal server error</html>`)
	})

	err := c.Publish(context.Background(), PublishRequest{Repo: "nixos", Bundle: []byte("bundle")})
	var rej *rules.Rejection
	if !errors.As(err, &rej) {
		t.Fatalf("error = %v, want a *rules.Rejection", err)
	}
	if rej.RuleID != rules.PublishUnavailable {
		t.Errorf("rule id = %q, want %q", rej.RuleID, rules.PublishUnavailable)
	}
}

// TestClientPublishRequestBody pins FR-33's wire body: repo, base64-encoded
// bundle bytes, bundle_sha256 and canonical_hash all survive the trip, and the
// body decodes back into the same PublishRequest.
func TestClientPublishRequestBody(t *testing.T) {
	type requestRecord struct {
		method      string
		path        string
		contentType string
		body        []byte
	}
	records := make(chan requestRecord, 1)

	c := newPublisherClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		records <- requestRecord{
			method:      r.Method,
			path:        r.URL.Path,
			contentType: r.Header.Get("Content-Type"),
			body:        body,
		}
		w.WriteHeader(http.StatusCreated)
	})

	req := PublishRequest{
		Repo:          "nixos",
		Bundle:        []byte{0x00, 0x01, 0xfe, 0xff, 'b', 'u', 'n', 'd', 'l', 'e'},
		BundleSHA256:  strings.Repeat("a", 64),
		CanonicalHash: strings.Repeat("b", 64),
	}
	if err := c.Publish(context.Background(), req); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	rec := <-records
	if rec.method != http.MethodPost {
		t.Errorf("method = %s, want POST", rec.method)
	}
	if rec.path != "/v1/publish" {
		t.Errorf("path = %q, want /v1/publish", rec.path)
	}
	if rec.contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", rec.contentType)
	}

	var wire map[string]json.RawMessage
	if err := json.Unmarshal(rec.body, &wire); err != nil {
		t.Fatalf("decode wire body %q: %v", rec.body, err)
	}
	if len(wire) != 4 {
		t.Errorf("wire body keys = %d (%v), want exactly repo, bundle, bundle_sha256, canonical_hash", len(wire), wire)
	}

	var wireRepo, wireBundle, wireSHA, wireHash string
	for key, dst := range map[string]*string{
		"repo":           &wireRepo,
		"bundle":         &wireBundle,
		"bundle_sha256":  &wireSHA,
		"canonical_hash": &wireHash,
	} {
		raw, ok := wire[key]
		if !ok {
			t.Fatalf("wire body is missing %q: %s", key, rec.body)
		}
		if err := json.Unmarshal(raw, dst); err != nil {
			t.Fatalf("decode %q: %v", key, err)
		}
	}
	if wireRepo != req.Repo {
		t.Errorf("repo = %q, want %q", wireRepo, req.Repo)
	}
	if want := base64.StdEncoding.EncodeToString(req.Bundle); wireBundle != want {
		t.Errorf("bundle wire value = %q, want base64 %q", wireBundle, want)
	}
	if wireSHA != req.BundleSHA256 {
		t.Errorf("bundle_sha256 = %q, want %q", wireSHA, req.BundleSHA256)
	}
	if wireHash != req.CanonicalHash {
		t.Errorf("canonical_hash = %q, want %q", wireHash, req.CanonicalHash)
	}

	var decoded PublishRequest
	if err := json.Unmarshal(rec.body, &decoded); err != nil {
		t.Fatalf("decode PublishRequest: %v", err)
	}
	if decoded.Repo != req.Repo {
		t.Errorf("decoded repo = %q, want %q", decoded.Repo, req.Repo)
	}
	if !bytes.Equal(decoded.Bundle, req.Bundle) {
		t.Errorf("decoded bundle = %v, want %v", decoded.Bundle, req.Bundle)
	}
	if decoded.BundleSHA256 != req.BundleSHA256 {
		t.Errorf("decoded bundle_sha256 = %q, want %q", decoded.BundleSHA256, req.BundleSHA256)
	}
	if decoded.CanonicalHash != req.CanonicalHash {
		t.Errorf("decoded canonical_hash = %q, want %q", decoded.CanonicalHash, req.CanonicalHash)
	}
}
