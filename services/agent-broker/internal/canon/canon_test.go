package canon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// canonKeys is the documented key set in sorted order (FR-29). Keeping the
// literal here makes a silent field addition show up as a failure.
var canonKeys = []string{"body", "commit_sha", "ref", "repo", "title", "tree_sha"}

func fixture() (repo, ref, commitSHA, treeSHA, title, body string) {
	return "nixos", "agent/6fa8e2f0-1c3d-4a5b-8c7d-9e0f1a2b3c4d/my-slug", "abc123", "def456", "a title", "a body"
}

// TestCanonicalJSONExactForm pins the encoding: sorted keys, compact separators,
// no insignificant whitespace. The fixture is ASCII so there is exactly one
// correct byte string and no escaping ambiguity (FR-29).
func TestCanonicalJSONExactForm(t *testing.T) {
	got, err := CanonicalJSON("nixos", "agent/u/x", "ccc", "ttt", "t", "b")
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	want := `{"body":"b","commit_sha":"ccc","ref":"agent/u/x","repo":"nixos","title":"t","tree_sha":"ttt"}`
	if string(got) != want {
		t.Errorf("CanonicalJSON =\n  %s\nwant\n  %s", got, want)
	}
}

// TestCanonicalJSONKeySet checks every documented key is present and nothing
// else is, so two different bundles cannot collide by omitting a field.
func TestCanonicalJSONKeySet(t *testing.T) {
	repo, ref, commitSHA, treeSHA, title, body := fixture()
	out, err := CanonicalJSON(repo, ref, commitSHA, treeSHA, title, body)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}

	var decoded map[string]string
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("CanonicalJSON output is not a JSON object: %v\n%s", err, out)
	}
	if len(decoded) != len(canonKeys) {
		t.Errorf("key count = %d, want %d: %s", len(decoded), len(canonKeys), out)
	}
	for _, k := range canonKeys {
		if _, ok := decoded[k]; !ok {
			t.Errorf("missing key %q in %s", k, out)
		}
	}
	if decoded["repo"] != repo || decoded["title"] != title || decoded["body"] != body {
		t.Errorf("values did not survive encoding: %#v", decoded)
	}
}

// TestCanonicalJSONKeysAreSorted covers the "keys sorted" half of FR-29. It
// inspects the raw byte order because a map round-trip normalises ordering away.
func TestCanonicalJSONKeysAreSorted(t *testing.T) {
	repo, ref, commitSHA, treeSHA, title, body := fixture()
	out, err := CanonicalJSON(repo, ref, commitSHA, treeSHA, title, body)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}

	prev := -1
	for _, k := range canonKeys {
		idx := strings.Index(string(out), `"`+k+`":`)
		if idx < 0 {
			t.Fatalf("key %q not found in %s", k, out)
		}
		if idx <= prev {
			t.Errorf("key %q appears before the previous key; output is not sorted: %s", k, out)
		}
		prev = idx
	}
}

// TestCanonicalJSONStable is the determinism half of FR-29: identical inputs
// must hash identically across calls and across processes.
func TestCanonicalJSONStable(t *testing.T) {
	repo, ref, commitSHA, treeSHA, title, body := fixture()

	first, err := CanonicalJSON(repo, ref, commitSHA, treeSHA, title, body)
	if err != nil {
		t.Fatalf("first CanonicalJSON: %v", err)
	}
	second, err := CanonicalJSON(repo, ref, commitSHA, treeSHA, title, body)
	if err != nil {
		t.Fatalf("second CanonicalJSON: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("CanonicalJSON is not deterministic:\n first = %s\nsecond = %s", first, second)
	}
}

// TestHashChangesWithEveryField covers FR-29's collision guarantee: one changed
// character in any covered field must change the hash.
func TestHashChangesWithEveryField(t *testing.T) {
	repo, ref, commitSHA, treeSHA, title, body := fixture()

	base, err := Hash(repo, ref, commitSHA, treeSHA, title, body)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if base == "" {
		t.Fatal("Hash returned an empty string")
	}

	tests := []struct {
		name         string
		repo, ref    string
		commit, tree string
		title, body  string
	}{
		{"repo", repo + "x", ref, commitSHA, treeSHA, title, body},
		{"ref", repo, ref + "x", commitSHA, treeSHA, title, body},
		{"commit_sha", repo, ref, commitSHA + "x", treeSHA, title, body},
		{"tree_sha", repo, ref, commitSHA, treeSHA + "x", title, body},
		{"title", repo, ref, commitSHA, treeSHA, title + "x", body},
		{"body", repo, ref, commitSHA, treeSHA, title, body + "x"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Hash(tc.repo, tc.ref, tc.commit, tc.tree, tc.title, tc.body)
			if err != nil {
				t.Fatalf("Hash: %v", err)
			}
			if got == base {
				t.Errorf("changing %s did not change the hash", tc.name)
			}
		})
	}
}

// TestBodyNewlineNormalisation covers FR-29: CRLF is normalised so the same
// content cannot hash two ways, but a trailing newline is real content and must
// still distinguish a file that ends with one.
func TestBodyNewlineNormalisation(t *testing.T) {
	repo, ref, commitSHA, treeSHA, title, _ := fixture()

	crlf, err := Hash(repo, ref, commitSHA, treeSHA, title, "a\r\nb")
	if err != nil {
		t.Fatalf("Hash(crlf): %v", err)
	}
	lf, err := Hash(repo, ref, commitSHA, treeSHA, title, "a\nb")
	if err != nil {
		t.Fatalf("Hash(lf): %v", err)
	}
	trailing, err := Hash(repo, ref, commitSHA, treeSHA, title, "a\nb\n")
	if err != nil {
		t.Fatalf("Hash(trailing): %v", err)
	}

	if crlf != lf {
		t.Errorf("CRLF body hashed %q, LF body hashed %q; want equal", crlf, lf)
	}
	if lf == trailing {
		t.Errorf("adding a trailing newline did not change the hash %q", lf)
	}
}

// TestHashIsSHA256OfCanonicalJSON keeps Hash and CanonicalJSON from drifting
// apart: the hash is defined over the canonical bytes, not some other encoding.
func TestHashIsSHA256OfCanonicalJSON(t *testing.T) {
	repo, ref, commitSHA, treeSHA, title, body := fixture()

	canonical, err := CanonicalJSON(repo, ref, commitSHA, treeSHA, title, body)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	want := fmt.Sprintf("%x", sha256.Sum256(canonical))

	got, err := Hash(repo, ref, commitSHA, treeSHA, title, body)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if got != want {
		t.Errorf("Hash = %q, want %q (sha256 of the canonical bytes)", got, want)
	}
	if len(got) != 64 {
		t.Errorf("Hash length = %d, want 64 hex characters", len(got))
	}
	if _, err := hex.DecodeString(got); err != nil {
		t.Errorf("Hash %q is not hex: %v", got, err)
	}
}

// TestBundleSHA256 covers FR-29's separate integrity hash: it is a plain SHA-256
// over the bundle bytes, independent of the canonical hash.
func TestBundleSHA256(t *testing.T) {
	tests := [][]byte{
		[]byte("bundle bytes"),
		{},
		[]byte{0x00, 0xff, 0x10},
	}
	for _, bundle := range tests {
		t.Run(fmt.Sprintf("len%d", len(bundle)), func(t *testing.T) {
			want := fmt.Sprintf("%x", sha256.Sum256(bundle))
			got := BundleSHA256(bundle)
			if got != want {
				t.Errorf("BundleSHA256(%q) = %q, want %q", bundle, got, want)
			}
			if len(got) != 64 {
				t.Errorf("BundleSHA256 length = %d, want 64", len(got))
			}
			if _, err := hex.DecodeString(got); err != nil {
				t.Errorf("BundleSHA256 = %q is not hex: %v", got, err)
			}
		})
	}
}
