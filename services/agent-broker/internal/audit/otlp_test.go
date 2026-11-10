package audit

// FR-52: audit records ship to the SigNoz collector path. These tests pin the
// OTLP/HTTP JSON shape and the rule that a collector outage never costs the
// local audit trail.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Minimal OTLP/HTTP JSON shapes. Only the fields this contract promises are
// decoded; unknown fields are ignored so the exporter may add resource hints.
type otlpValue struct {
	StringValue string `json:"stringValue"`
	ArrayValue  *struct {
		Values []otlpValue `json:"values"`
	} `json:"arrayValue"`
}

type otlpAttribute struct {
	Key   string    `json:"key"`
	Value otlpValue `json:"value"`
}

type otlpLogRecord struct {
	Body       json.RawMessage `json:"body"`
	Attributes []otlpAttribute `json:"attributes"`
}

type otlpPayload struct {
	ResourceLogs []struct {
		Resource struct {
			Attributes []otlpAttribute `json:"attributes"`
		} `json:"resource"`
		ScopeLogs []struct {
			LogRecords []otlpLogRecord `json:"logRecords"`
		} `json:"scopeLogs"`
	} `json:"resourceLogs"`
}

type otlpRequest struct {
	method      string
	path        string
	contentType string
	body        []byte
}

// newOTLPServer records every request and answers with status.
func newOTLPServer(t *testing.T, status int) (*httptest.Server, <-chan otlpRequest) {
	t.Helper()
	requests := make(chan otlpRequest, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- otlpRequest{
			method:      r.Method,
			path:        r.URL.Path,
			contentType: r.Header.Get("Content-Type"),
			body:        body,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"partialSuccess":{}}`)
	}))
	t.Cleanup(srv.Close)
	return srv, requests
}

func otlpNextRequest(t *testing.T, requests <-chan otlpRequest) otlpRequest {
	t.Helper()
	select {
	case req := <-requests:
		return req
	case <-time.After(2 * time.Second):
		t.Fatal("no OTLP request reached the server")
		return otlpRequest{}
	}
}

func otlpAttrs(attrs []otlpAttribute) map[string]otlpValue {
	m := make(map[string]otlpValue, len(attrs))
	for _, attr := range attrs {
		m[attr.Key] = attr.Value
	}
	return m
}

func otlpString(t *testing.T, attrs map[string]otlpValue, key string) string {
	t.Helper()
	value, ok := attrs[key]
	if !ok {
		t.Fatalf("missing OTLP attribute %q (have %v)", key, otlpKeys(attrs))
	}
	if value.StringValue == "" {
		t.Fatalf("OTLP attribute %q is not a non-empty string: %#v", key, value)
	}
	return value.StringValue
}

func otlpKeys(attrs map[string]otlpValue) []string {
	keys := make([]string, 0, len(attrs))
	for key := range attrs {
		keys = append(keys, key)
	}
	return keys
}

func otlpDecode(t *testing.T, body []byte) otlpPayload {
	t.Helper()
	var payload otlpPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode OTLP body: %v (body %q)", err, body)
	}
	return payload
}

func otlpOnlyLogRecord(t *testing.T, payload otlpPayload) otlpLogRecord {
	t.Helper()
	if len(payload.ResourceLogs) != 1 {
		t.Fatalf("resourceLogs entries = %d, want 1", len(payload.ResourceLogs))
	}
	var records []otlpLogRecord
	for _, scope := range payload.ResourceLogs[0].ScopeLogs {
		records = append(records, scope.LogRecords...)
	}
	if len(records) != 1 {
		t.Fatalf("logRecords = %d, want exactly 1", len(records))
	}
	return records[0]
}

func assertRuleOutcomes(t *testing.T, attrs map[string]otlpValue, want []string) {
	t.Helper()
	value, ok := attrs["rule_outcomes"]
	if !ok {
		t.Fatalf("missing OTLP attribute rule_outcomes (have %v)", otlpKeys(attrs))
	}
	switch {
	case value.ArrayValue != nil:
		got := make([]string, 0, len(value.ArrayValue.Values))
		for _, item := range value.ArrayValue.Values {
			got = append(got, item.StringValue)
		}
		if len(got) != len(want) {
			t.Fatalf("rule_outcomes = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("rule_outcomes = %v, want %v", got, want)
			}
		}
	case value.StringValue != "":
		// A string encoding is allowed; it must still carry every outcome.
		for _, outcome := range want {
			if !strings.Contains(value.StringValue, outcome) {
				t.Errorf("rule_outcomes %q does not contain %q", value.StringValue, outcome)
			}
		}
	default:
		t.Fatalf("rule_outcomes is neither an array of strings nor a string: %#v", value)
	}
}

// TestNewOTLPExporterEmptyEndpoint covers the no-collector configuration: no
// exporter, no network calls.
func TestNewOTLPExporterEmptyEndpoint(t *testing.T) {
	if got := NewOTLPExporter("", "validator"); got != nil {
		t.Fatalf("NewOTLPExporter(\"\", ...) = %#v, want nil", got)
	}
}

// TestWriteContextSkipsNilExporter covers the nil-Exporter path: the local
// record is still written and no panic occurs.
func TestWriteContextSkipsNilExporter(t *testing.T) {
	var out bytes.Buffer
	l := New(&out)
	l.Now = unit5Now
	l.Exporter = nil

	if err := l.WriteContext(context.Background(), unit5Record()); err != nil {
		t.Fatalf("WriteContext with nil exporter: %v", err)
	}
	if out.Len() == 0 {
		t.Fatal("stdout is empty with a nil exporter")
	}
	if !strings.HasSuffix(out.String(), "\n") {
		t.Errorf("stdout is not newline-terminated: %q", out.String())
	}
}

// TestOTLPExporterPostsOneLogRecord covers FR-52's shipment contract: one POST
// to /v1/logs, one resourceLogs entry carrying the service name, and exactly
// one logRecord whose attributes mirror the FR-51 field set.
func TestOTLPExporterPostsOneLogRecord(t *testing.T) {
	srv, requests := newOTLPServer(t, http.StatusOK)
	exporter := NewOTLPExporter(srv.URL, "validator")
	if exporter == nil {
		t.Fatal("NewOTLPExporter returned nil for a non-empty endpoint")
	}
	if exporter.Service != "validator" {
		t.Fatalf("Service = %q, want validator", exporter.Service)
	}

	var out bytes.Buffer
	l := New(&out)
	l.Now = unit5Now
	l.Exporter = exporter
	rec := unit5Record()

	if err := l.WriteContext(context.Background(), rec); err != nil {
		t.Fatalf("WriteContext: %v", err)
	}
	if out.Len() == 0 {
		t.Fatal("stdout is empty when OTLP export succeeded")
	}

	req := otlpNextRequest(t, requests)
	if req.method != http.MethodPost {
		t.Errorf("method = %q, want POST", req.method)
	}
	if req.path != "/v1/logs" {
		t.Errorf("path = %q, want /v1/logs", req.path)
	}
	mediaType, _, err := mime.ParseMediaType(req.contentType)
	if err != nil {
		t.Fatalf("parse Content-Type %q: %v", req.contentType, err)
	}
	if mediaType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", req.contentType)
	}

	payload := otlpDecode(t, req.body)
	if len(payload.ResourceLogs) != 1 {
		t.Fatalf("resourceLogs entries = %d, want 1", len(payload.ResourceLogs))
	}
	resourceAttrs := otlpAttrs(payload.ResourceLogs[0].Resource.Attributes)
	if got := otlpString(t, resourceAttrs, "service.name"); got != "validator" {
		t.Errorf("resource service.name = %q, want validator", got)
	}

	record := otlpOnlyLogRecord(t, payload)
	if len(record.Body) == 0 || string(record.Body) == "null" || string(record.Body) == "{}" {
		t.Errorf("log record body is empty: %q", record.Body)
	}

	attrs := otlpAttrs(record.Attributes)
	want := map[string]string{
		"request_id":     rec.RequestID,
		"sandbox_uid":    rec.SandboxUID,
		"repo":           rec.Repo,
		"ref":            rec.Ref,
		"base_commit":    rec.BaseCommit,
		"commit_sha":     rec.CommitSHA,
		"tree_sha":       rec.TreeSHA,
		"bundle_sha256":  rec.BundleSHA256,
		"canonical_hash": rec.CanonicalHash,
		"decision":       rec.Decision,
		"rule_id":        rec.RuleID,
		"duration_ms":    strconv.FormatInt(rec.DurationMS, 10),
	}
	for key, wantValue := range want {
		if got := otlpString(t, attrs, key); got != wantValue {
			t.Errorf("attribute %s = %q, want %q", key, got, wantValue)
		}
	}
	assertRuleOutcomes(t, attrs, rec.RuleOutcomes)

	// Exactly one POST: a second request is a duplicate audit record.
	time.Sleep(20 * time.Millisecond)
	select {
	case extra := <-requests:
		t.Fatalf("second OTLP request: %+v", extra)
	default:
	}
}

// TestOTLPRedactsAttributeValues covers FR-51 on the shipment path: a secret in
// a record field must not appear in the OTLP body.
func TestOTLPRedactsAttributeValues(t *testing.T) {
	srv, requests := newOTLPServer(t, http.StatusOK)
	exporter := NewOTLPExporter(srv.URL, "validator")
	if exporter == nil {
		t.Fatal("NewOTLPExporter returned nil for a non-empty endpoint")
	}

	var out bytes.Buffer
	l := New(&out)
	l.Now = unit5Now
	l.Exporter = exporter
	rec := unit5Record()
	rec.Repo = unit5GitHubToken
	rec.Reason = unit5Password

	if err := l.WriteContext(context.Background(), rec); err != nil {
		t.Fatalf("WriteContext: %v", err)
	}

	req := otlpNextRequest(t, requests)
	if bytes.Contains(req.body, []byte("ghp_")) {
		t.Errorf("OTLP body leaked a GitHub token: %q", req.body)
	}
	if bytes.Contains(req.body, []byte("hunter2")) {
		t.Errorf("OTLP body leaked a password: %q", req.body)
	}

	payload := otlpDecode(t, req.body)
	record := otlpOnlyLogRecord(t, payload)
	attrs := otlpAttrs(record.Attributes)
	if got := otlpString(t, attrs, "repo"); got != "[REDACTED]" {
		t.Errorf("repo attribute = %q, want [REDACTED]", got)
	}
	if value, ok := attrs["reason"]; ok && value.StringValue != "" && value.StringValue != "[REDACTED]" {
		t.Errorf("reason attribute = %q, want [REDACTED]", value.StringValue)
	}
}

// TestOTLPExportFailureDoesNotFailWrite covers FR-52's outage rule: the
// collector is best-effort, the local record is not.
func TestOTLPExportFailureDoesNotFailWrite(t *testing.T) {
	cases := []struct {
		name     string
		endpoint func(t *testing.T) string
	}{
		{
			name: "collector returns 500",
			endpoint: func(t *testing.T) string {
				srv, _ := newOTLPServer(t, http.StatusInternalServerError)
				return srv.URL
			},
		},
		{
			name: "collector is unreachable",
			endpoint: func(t *testing.T) string {
				srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
				endpoint := srv.URL
				srv.Close()
				return endpoint
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exporter := NewOTLPExporter(tc.endpoint(t), "validator")
			if exporter == nil {
				t.Fatal("NewOTLPExporter returned nil for a non-empty endpoint")
			}

			var out bytes.Buffer
			l := New(&out)
			l.Now = unit5Now
			l.Exporter = exporter

			if err := l.WriteContext(context.Background(), unit5Record()); err != nil {
				t.Fatalf("WriteContext returned an error when OTLP failed: %v", err)
			}
			if out.Len() == 0 {
				t.Fatal("stdout was not written when OTLP failed")
			}
			if got := strings.Count(out.String(), "\n"); got != 1 {
				t.Errorf("stdout newline count = %d, want 1: %q", got, out.String())
			}
		})
	}
}
