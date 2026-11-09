package audit

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/rules"
)

// documentedKeys is the log schema from the package doc (FR-31). It is repeated
// literally so renaming a struct tag is a test failure, not a silent log change
// consumers would only notice in production.
var documentedKeys = []string{
	"request_id",
	"sandbox_uid",
	"repo",
	"ref",
	"base_commit",
	"commit_sha",
	"tree_sha",
	"bundle_sha256",
	"canonical_hash",
	"decision",
	"rule_id",
	"duration_ms",
	"time",
}

func fixedClock() time.Time {
	return time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
}

func sampleRecord() Record {
	return Record{
		RequestID:     "req-1",
		SandboxUID:    "6fa8e2f0-1c3d-4a5b-8c7d-9e0f1a2b3c4d",
		Repo:          "nixos",
		Ref:           "agent/6fa8e2f0-1c3d-4a5b-8c7d-9e0f1a2b3c4d/my-slug",
		BaseCommit:    "base123",
		CommitSHA:     "commit123",
		TreeSHA:       "tree123",
		BundleSHA256:  strings.Repeat("a", 64),
		CanonicalHash: strings.Repeat("b", 64),
		Decision:      Accept,
		DurationMS:    42,
	}
}

func writeOne(t *testing.T, r Record) string {
	t.Helper()
	var buf bytes.Buffer
	l := New(&buf)
	l.Now = fixedClock
	if err := l.Write(r); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return buf.String()
}

// TestWriteEmitsOneJSONLine covers FR-31's NDJSON contract: exactly one record
// per request, exactly one line, newline-terminated so `kubectl logs` splits it.
func TestWriteEmitsOneJSONLine(t *testing.T) {
	out := writeOne(t, sampleRecord())

	if !strings.HasSuffix(out, "\n") {
		t.Errorf("record does not end with a newline: %q", out)
	}
	if got := strings.Count(out, "\n"); got != 1 {
		t.Errorf("newline count = %d, want 1: %q", got, out)
	}

	line := strings.TrimSuffix(out, "\n")
	if strings.ContainsAny(line, "\n\r") {
		t.Fatalf("record spans more than one line: %q", line)
	}
	if !json.Valid([]byte(line)) {
		t.Fatalf("record is not valid JSON: %q", line)
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		t.Fatalf("record does not parse as one JSON object: %v", err)
	}
}

// TestWriteOncePerRequest guards FR-31's "accepted or rejected, never both":
// two writes must produce two independent lines, so a caller cannot accumulate a
// multi-line blob in one record.
func TestWriteOncePerRequest(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf)
	l.Now = fixedClock

	if err := l.Write(sampleRecord()); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	rejected := sampleRecord()
	rejected.Decision = Reject
	rejected.RuleID = rules.SecretDetected
	if err := l.Write(rejected); err != nil {
		t.Fatalf("second Write: %v", err)
	}

	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("line count = %d, want 2: %q", len(lines), buf.String())
	}
	for i, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Errorf("line %d is not valid JSON: %q", i, line)
		}
	}
}

// TestRecordCarriesInjectedClock covers FR-31's timestamp field: the logger must
// stamp the record from its clock, not from the caller's zero time.
func TestRecordCarriesInjectedClock(t *testing.T) {
	out := writeOne(t, sampleRecord())

	var decoded struct {
		Time time.Time `json:"time"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &decoded); err != nil {
		t.Fatalf("decode record: %v", err)
	}
	if !decoded.Time.Equal(fixedClock()) {
		t.Errorf("time = %s, want %s", decoded.Time, fixedClock())
	}
}

// TestKeySetMatchesStructTags covers FR-31's "no key is omitted". Comparing
// against the struct's own tags keeps the fixture honest when the schema grows.
func TestKeySetMatchesStructTags(t *testing.T) {
	out := writeOne(t, sampleRecord())

	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &obj); err != nil {
		t.Fatalf("decode record: %v", err)
	}

	typ := reflect.TypeOf(Record{})
	want := make(map[string]bool, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if tag == "" || name == "" || name == "-" {
			t.Errorf("field %s has no usable json tag: %q", typ.Field(i).Name, tag)
			continue
		}
		want[name] = true
	}

	if len(obj) != len(want) {
		t.Errorf("record has %d keys, want %d: %v", len(obj), len(want), obj)
	}
	for name := range want {
		if _, ok := obj[name]; !ok {
			t.Errorf("record is missing key %q", name)
		}
	}
	for name := range obj {
		if !want[name] {
			t.Errorf("record has undocumented key %q", name)
		}
	}

	for _, name := range documentedKeys {
		if _, ok := obj[name]; !ok {
			t.Errorf("record is missing documented key %q", name)
		}
	}
}

// TestRejectRoundTrip covers FR-31's error path: a rejection carries the rule id
// so dashboards can group by cause, surviving the JSON round-trip unchanged.
func TestRejectRoundTrip(t *testing.T) {
	in := sampleRecord()
	in.Decision = Reject
	in.RuleID = rules.PathNotAllowed
	in.CommitSHA = ""
	in.TreeSHA = ""
	in.BundleSHA256 = ""

	out := writeOne(t, in)

	var got Record
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
		t.Fatalf("decode record: %v", err)
	}
	if got.Decision != Reject {
		t.Errorf("Decision = %q, want %q", got.Decision, Reject)
	}
	if got.RuleID != rules.PathNotAllowed {
		t.Errorf("RuleID = %q, want %q", got.RuleID, rules.PathNotAllowed)
	}
	if !got.Time.Equal(fixedClock()) {
		t.Errorf("Time = %s, want %s", got.Time, fixedClock())
	}

	// The acceptance fields must still be present, even when empty, so log
	// consumers can rely on a fixed schema.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &obj); err != nil {
		t.Fatalf("decode object: %v", err)
	}
	for _, name := range documentedKeys {
		if _, ok := obj[name]; !ok {
			t.Errorf("rejection record is missing key %q", name)
		}
	}
}
