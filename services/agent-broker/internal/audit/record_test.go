package audit

// FR-51/FR-52: the record carries the full request shape, and no secret
// material may reach any echo path (stdout, the local store, or OTLP).

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// unit5Now is a fixed clock local to the unit-5 tests so they do not depend on
// the fixtures in audit_test.go.
func unit5Now() time.Time {
	return time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
}

// fr51Keys is the FR-51 field set written literally. A struct field added
// without a corresponding entry here is a test failure, not a silent schema
// drift consumers would only notice in production.
var fr51Keys = []string{
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
	"reason",
	"rule_outcomes",
	"duration_ms",
	"time",
}

func unit5Record() Record {
	return Record{
		RequestID:     "req-fr51",
		SandboxUID:    "6fa8e2f0-1c3d-4a5b-8c7d-9e0f1a2b3c4d",
		Repo:          "homelab-cluster",
		Ref:           "agent/6fa8e2f0-1c3d-4a5b-8c7d-9e0f1a2b3c4d/unit-5",
		BaseCommit:    "0123456789abcdef0123456789abcdef01234567",
		CommitSHA:     "89abcdef0123456789abcdef0123456789abcdef",
		TreeSHA:       "fedcba9876543210fedcba9876543210fedcba98",
		BundleSHA256:  strings.Repeat("a", 64),
		CanonicalHash: strings.Repeat("b", 64),
		Decision:      Reject,
		RuleID:        "path-not-allowed",
		Reason:        "path is outside the allowlist",
		RuleOutcomes:  []string{"path-allowlist", "secret-scan"},
		DurationMS:    42,
	}
}

// unit5SecretShapes are the evasion shapes named by FR-51. Each value is a
// complete secret (not an embedded fragment), so a fully redacted field must be
// exactly [REDACTED].
var (
	unit5PEMPrivateKey = "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA7bq9\n-----END RSA PRIVATE KEY-----"
	unit5GitHubToken   = "ghp_" + strings.Repeat("a", 36)
	unit5AWSAccessKey  = "AKIA" + "ABCDEFGHIJKLMNOP"
	unit5JWT           = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"
	unit5SSHKey        = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJd8h2r9v0f1n4x7c2m5k8p3s6t9w2y5b8e1r4u7i0o3"
	unit5Password      = "password = hunter2"
)

type unit5SecretShape struct {
	name  string
	value string
}

func unit5SecretShapes() []unit5SecretShape {
	return []unit5SecretShape{
		{"pem-private-key", unit5PEMPrivateKey},
		{"github-token", unit5GitHubToken},
		{"aws-access-key", unit5AWSAccessKey},
		{"jwt", unit5JWT},
		{"ssh-ed25519", unit5SSHKey},
		{"password-assignment", unit5Password},
	}
}

// unit5EchoField names one string field and how to address it after a JSON
// round-trip. index is -1 for a scalar and >= 0 for a rule_outcomes element.
type unit5EchoField struct {
	name  string
	key   string
	index int
	set   func(*Record, string)
}

func unit5EchoFields() []unit5EchoField {
	return []unit5EchoField{
		{"RequestID", "request_id", -1, func(r *Record, v string) { r.RequestID = v }},
		{"SandboxUID", "sandbox_uid", -1, func(r *Record, v string) { r.SandboxUID = v }},
		{"Repo", "repo", -1, func(r *Record, v string) { r.Repo = v }},
		{"Ref", "ref", -1, func(r *Record, v string) { r.Ref = v }},
		{"BaseCommit", "base_commit", -1, func(r *Record, v string) { r.BaseCommit = v }},
		{"CommitSHA", "commit_sha", -1, func(r *Record, v string) { r.CommitSHA = v }},
		{"TreeSHA", "tree_sha", -1, func(r *Record, v string) { r.TreeSHA = v }},
		{"BundleSHA256", "bundle_sha256", -1, func(r *Record, v string) { r.BundleSHA256 = v }},
		{"CanonicalHash", "canonical_hash", -1, func(r *Record, v string) { r.CanonicalHash = v }},
		{"Decision", "decision", -1, func(r *Record, v string) { r.Decision = v }},
		{"RuleID", "rule_id", -1, func(r *Record, v string) { r.RuleID = v }},
		{"Reason", "reason", -1, func(r *Record, v string) { r.Reason = v }},
		{"RuleOutcomes[0]", "rule_outcomes", 0, func(r *Record, v string) {
			r.RuleOutcomes = []string{v, "secret-scan"}
		}},
		{"RuleOutcomes[1]", "rule_outcomes", 1, func(r *Record, v string) {
			r.RuleOutcomes = []string{"path-allowlist", v}
		}},
	}
}

type unit5CaptureExporter struct {
	mu      sync.Mutex
	records []Record
	err     error
}

func (c *unit5CaptureExporter) Export(_ context.Context, r Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, r)
	return c.err
}

func (c *unit5CaptureExporter) captured() []Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Record, len(c.records))
	copy(out, c.records)
	return out
}

func unit5DecodeObject(t *testing.T, line string) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		t.Fatalf("decode %q: %v", line, err)
	}
	return obj
}

func unit5StringAt(t *testing.T, obj map[string]any, key string, index int) string {
	t.Helper()
	v, ok := obj[key]
	if !ok {
		t.Fatalf("record is missing key %q: %v", key, obj)
	}
	if index < 0 {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("key %q is %T, want string", key, v)
		}
		return s
	}
	arr, ok := v.([]any)
	if !ok {
		t.Fatalf("key %q is %T, want array", key, v)
	}
	if index >= len(arr) {
		t.Fatalf("key %q has %d elements, want index %d", key, len(arr), index)
	}
	s, ok := arr[index].(string)
	if !ok {
		t.Fatalf("key %q[%d] is %T, want string", key, index, arr[index])
	}
	return s
}

func unit5Write(t *testing.T, r Record) string {
	t.Helper()
	var buf bytes.Buffer
	l := New(&buf)
	l.Now = unit5Now
	if err := l.Write(r); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return buf.String()
}

// TestRedactMasksSecretShapes covers FR-51's "no secret material" requirement:
// every named secret shape becomes [REDACTED], while benign identifiers that
// merely look adjacent to the patterns are untouched.
func TestRedactMasksSecretShapes(t *testing.T) {
	const redacted = "[REDACTED]"

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"pem private key", unit5PEMPrivateKey, redacted},
		{"github token", unit5GitHubToken, redacted},
		{"aws access key", unit5AWSAccessKey, redacted},
		{"jwt", unit5JWT, redacted},
		{"ssh ed25519 key", unit5SSHKey, redacted},
		{"password assignment", unit5Password, redacted},
		{"github token inside prose", "config token " + unit5GitHubToken + " here", "config token " + redacted + " here"},
		{"empty string", "", ""},
		{"uuid unchanged", "6fa8e2f0-1c3d-4a5b-8c7d-9e0f1a2b3c4d", "6fa8e2f0-1c3d-4a5b-8c7d-9e0f1a2b3c4d"},
		{"forty char git sha unchanged", "0123456789abcdef0123456789abcdef01234567", "0123456789abcdef0123456789abcdef01234567"},
		{"sixty four char hash unchanged", strings.Repeat("c", 64), strings.Repeat("c", 64)},
		{"token bucket unchanged", "token bucket", "token bucket"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Redact(tc.in); got != tc.want {
				t.Errorf("Redact(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestFR51FieldSetWrittenLiterally pins the FR-51 schema. It compares the
// emitted object against a literal list rather than the struct tags so a
// renamed or dropped field cannot slip through.
func TestFR51FieldSetWrittenLiterally(t *testing.T) {
	out := unit5Write(t, unit5Record())
	obj := unit5DecodeObject(t, strings.TrimSuffix(out, "\n"))

	want := make(map[string]bool, len(fr51Keys))
	for _, key := range fr51Keys {
		want[key] = true
		if _, ok := obj[key]; !ok {
			t.Errorf("record is missing FR-51 key %q", key)
		}
	}
	if len(obj) != len(want) {
		t.Errorf("record has %d keys, want %d: %v", len(obj), len(want), obj)
	}
	for key := range obj {
		if !want[key] {
			t.Errorf("record has undocumented key %q", key)
		}
	}
}

// TestEveryRecordCarriesReasonAndRuleOutcomes covers FR-51's field set on the
// empty and populated paths: consumers may rely on the keys always existing.
func TestEveryRecordCarriesReasonAndRuleOutcomes(t *testing.T) {
	empty := unit5Record()
	empty.Reason = ""
	empty.RuleOutcomes = nil

	cases := []struct {
		name string
		rec  Record
	}{
		{"populated", unit5Record()},
		{"empty", empty},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := unit5Write(t, tc.rec)
			if !strings.HasSuffix(out, "\n") {
				t.Errorf("record does not end with a newline: %q", out)
			}
			if got := strings.Count(out, "\n"); got != 1 {
				t.Errorf("newline count = %d, want 1: %q", got, out)
			}

			obj := unit5DecodeObject(t, strings.TrimSuffix(out, "\n"))
			for _, key := range []string{"reason", "rule_outcomes"} {
				if _, ok := obj[key]; !ok {
					t.Errorf("record is missing %q key: %v", key, obj)
				}
			}
		})
	}
}

// TestWriteIsOneLinePerRecordWithFR51Fields covers FR-51's NDJSON contract for
// more than one request: each line is independent and complete.
func TestWriteIsOneLinePerRecordWithFR51Fields(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf)
	l.Now = unit5Now

	if err := l.Write(unit5Record()); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	accepted := unit5Record()
	accepted.Decision = Accept
	accepted.RuleID = ""
	accepted.Reason = ""
	if err := l.Write(accepted); err != nil {
		t.Fatalf("second Write: %v", err)
	}

	if !strings.HasSuffix(buf.String(), "\n") {
		t.Errorf("NDJSON is not newline-terminated: %q", buf.String())
	}
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("line count = %d, want 2: %q", len(lines), buf.String())
	}
	for i, line := range lines {
		obj := unit5DecodeObject(t, line)
		for _, key := range fr51Keys {
			if _, ok := obj[key]; !ok {
				t.Errorf("line %d is missing FR-51 key %q", i, key)
			}
		}
	}
}

// TestRedactAppliesToEveryEchoPath covers FR-51's no-leak guarantee on every
// path the record reaches. Each string field is filled with each secret shape,
// then stdout, the store file, and the exporter are inspected. A fake exporter
// is used here so the assertion is about the Record handed to OTLP, not the
// wire encoding.
func TestRedactAppliesToEveryEchoPath(t *testing.T) {
	const redacted = "[REDACTED]"

	for _, field := range unit5EchoFields() {
		for _, secret := range unit5SecretShapes() {
			t.Run(field.name+"/"+secret.name, func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "nested", "audit.ndjson")
				if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
					t.Fatalf("precondition: parent directory already exists: %v", err)
				}

				store, err := OpenStore(path, 72*time.Hour)
				if err != nil {
					t.Fatalf("OpenStore: %v", err)
				}
				store.Now = unit5Now

				var stdout bytes.Buffer
				exporter := &unit5CaptureExporter{}
				l := New(&stdout)
				l.Now = unit5Now
				l.Store = store
				l.Exporter = exporter

				rec := unit5Record()
				field.set(&rec, secret.value)
				if err := l.WriteContext(context.Background(), rec); err != nil {
					t.Fatalf("WriteContext: %v", err)
				}
				if err := store.Close(); err != nil {
					t.Fatalf("Close store: %v", err)
				}
				if _, err := os.Stat(filepath.Dir(path)); err != nil {
					t.Fatalf("Store did not create parent directories: %v", err)
				}

				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read store: %v", err)
				}

				for pathName, line := range map[string]string{
					"stdout": stdout.String(),
					"store":  string(data),
				} {
					obj := unit5DecodeObject(t, strings.TrimSpace(line))
					if got := unit5StringAt(t, obj, field.key, field.index); got != redacted {
						t.Errorf("%s %s = %q, want %q", pathName, field.name, got, redacted)
					}
				}

				captured := exporter.captured()
				if len(captured) != 1 {
					t.Fatalf("exporter saw %d records, want 1", len(captured))
				}
				raw, err := json.Marshal(captured[0])
				if err != nil {
					t.Fatalf("marshal exported record: %v", err)
				}
				obj := unit5DecodeObject(t, string(raw))
				if got := unit5StringAt(t, obj, field.key, field.index); got != redacted {
					t.Errorf("exporter %s = %q, want %q", field.name, got, redacted)
				}
			})
		}
	}
}

// TestStorePrunesByRecordTime covers FR-52's retention requirement: the store
// compares each record's own time against its current clock minus Retention. A
// record outside the window is dropped when a later append exposes it.
func TestStorePrunesByRecordTime(t *testing.T) {
	base := unit5Now()
	const retention = 72 * time.Hour

	cases := []struct {
		name    string
		age     time.Duration
		wantOld bool
	}{
		{"older than retention is pruned", 73 * time.Hour, false},
		{"inside retention is kept", 71 * time.Hour, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "nested", "deeper", "audit.ndjson")
			if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
				t.Fatalf("precondition: parent directory already exists: %v", err)
			}

			store, err := OpenStore(path, retention)
			if err != nil {
				t.Fatalf("OpenStore: %v", err)
			}
			store.Now = func() time.Time { return base }

			var stdout bytes.Buffer
			l := New(&stdout)
			l.Store = store
			now := base.Add(-tc.age)
			l.Now = func() time.Time { return now }

			old := unit5Record()
			old.RequestID = "old-record"
			if err := l.Write(old); err != nil {
				t.Fatalf("Write old record: %v", err)
			}

			now = base
			fresh := unit5Record()
			fresh.RequestID = "fresh-record"
			if err := l.Write(fresh); err != nil {
				t.Fatalf("Write fresh record: %v", err)
			}

			if err := store.Close(); err != nil {
				t.Fatalf("Close store: %v", err)
			}
			if _, err := os.Stat(filepath.Dir(path)); err != nil {
				t.Fatalf("Store did not create parent directories: %v", err)
			}

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read store: %v", err)
			}
			if len(data) == 0 || data[len(data)-1] != '\n' {
				t.Fatalf("store is not newline-terminated NDJSON: %q", data)
			}

			lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
			var ids []string
			for _, line := range lines {
				if line == "" {
					t.Fatalf("store contains a blank line: %q", data)
				}
				obj := unit5DecodeObject(t, line)
				id, ok := obj["request_id"].(string)
				if !ok {
					t.Fatalf("stored line has no request_id: %q", line)
				}
				ids = append(ids, id)
			}

			want := []string{"fresh-record"}
			if tc.wantOld {
				want = []string{"old-record", "fresh-record"}
			}
			if len(ids) != len(want) {
				t.Fatalf("stored ids = %v, want %v", ids, want)
			}
			for i := range want {
				if ids[i] != want[i] {
					t.Fatalf("stored ids = %v, want %v", ids, want)
				}
			}
		})
	}
}
