package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// OTLPExporter posts one OTLP/HTTP JSON log record per audit record (FR-52).
type OTLPExporter struct {
	Endpoint string
	Service  string
	Client   *http.Client
}

// NewOTLPExporter returns nil when endpoint is empty: no collector is
// configured, so no network calls are made.
func NewOTLPExporter(endpoint, service string) *OTLPExporter {
	if strings.TrimSpace(endpoint) == "" {
		return nil
	}
	return &OTLPExporter{
		Endpoint: endpoint,
		Service:  service,
		Client:   &http.Client{Timeout: 5 * time.Second},
	}
}

// OTLP/HTTP JSON shapes. Only the fields this contract promises are emitted.
type otlpJSONValue struct {
	StringValue string         `json:"stringValue,omitempty"`
	ArrayValue  *otlpJSONArray `json:"arrayValue,omitempty"`
}

type otlpJSONArray struct {
	Values []otlpJSONValue `json:"values"`
}

type otlpJSONAttribute struct {
	Key   string        `json:"key"`
	Value otlpJSONValue `json:"value"`
}

type otlpJSONLogRecord struct {
	TimeUnixNano string              `json:"timeUnixNano,omitempty"`
	Body         otlpJSONValue       `json:"body"`
	Attributes   []otlpJSONAttribute `json:"attributes"`
}

type otlpJSONScopeLogs struct {
	Scope      map[string]string   `json:"scope,omitempty"`
	LogRecords []otlpJSONLogRecord `json:"logRecords"`
}

type otlpJSONResource struct {
	Attributes []otlpJSONAttribute `json:"attributes"`
}

type otlpJSONResourceLogs struct {
	Resource  otlpJSONResource    `json:"resource"`
	ScopeLogs []otlpJSONScopeLogs `json:"scopeLogs"`
}

type otlpJSONPayload struct {
	ResourceLogs []otlpJSONResourceLogs `json:"resourceLogs"`
}

// Export posts exactly one log record to <endpoint>/v1/logs.
func (e *OTLPExporter) Export(ctx context.Context, r Record) error {
	if e == nil {
		return nil
	}
	// Redact defensively: the logger already does this, but a direct caller
	// must not be able to put a secret on the wire.
	r = RedactRecord(r)

	body, err := json.Marshal(r)
	if err != nil {
		return err
	}

	attrs := []otlpJSONAttribute{
		stringAttr("request_id", r.RequestID),
		stringAttr("sandbox_uid", r.SandboxUID),
		stringAttr("repo", r.Repo),
		stringAttr("ref", r.Ref),
		stringAttr("base_commit", r.BaseCommit),
		stringAttr("commit_sha", r.CommitSHA),
		stringAttr("tree_sha", r.TreeSHA),
		stringAttr("bundle_sha256", r.BundleSHA256),
		stringAttr("canonical_hash", r.CanonicalHash),
		stringAttr("decision", r.Decision),
		stringAttr("rule_id", r.RuleID),
		stringAttr("reason", r.Reason),
		stringAttr("duration_ms", strconv.FormatInt(r.DurationMS, 10)),
		stringAttr("time", r.Time.UTC().Format(time.RFC3339Nano)),
		ruleOutcomesAttr(r.RuleOutcomes),
	}

	payload := otlpJSONPayload{
		ResourceLogs: []otlpJSONResourceLogs{{
			Resource: otlpJSONResource{Attributes: []otlpJSONAttribute{
				stringAttr("service.name", e.Service),
			}},
			ScopeLogs: []otlpJSONScopeLogs{{
				Scope: map[string]string{"name": "agent-broker"},
				LogRecords: []otlpJSONLogRecord{{
					TimeUnixNano: strconv.FormatInt(r.Time.UnixNano(), 10),
					Body:         otlpJSONValue{StringValue: string(body)},
					Attributes:   attrs,
				}},
			}},
		}},
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	url := strings.TrimRight(e.Endpoint, "/") + "/v1/logs"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	client := e.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("audit: OTLP collector returned %s", resp.Status)
	}
	return nil
}

func stringAttr(key, value string) otlpJSONAttribute {
	return otlpJSONAttribute{Key: key, Value: otlpJSONValue{StringValue: value}}
}

func ruleOutcomesAttr(outcomes []string) otlpJSONAttribute {
	values := make([]otlpJSONValue, 0, len(outcomes))
	for _, outcome := range outcomes {
		values = append(values, otlpJSONValue{StringValue: Redact(outcome)})
	}
	return otlpJSONAttribute{
		Key:   "rule_outcomes",
		Value: otlpJSONValue{ArrayValue: &otlpJSONArray{Values: values}},
	}
}
