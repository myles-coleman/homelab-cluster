package commitmeta

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestEncodeParseRoundTrip covers FR-34: the metadata carrier appended to a
// validator commit must survive a round trip through Encode/Parse without
// mangling quotes, newlines, or the four fields the canonical hash covers.
func TestEncodeParseRoundTrip(t *testing.T) {
	want := Metadata{
		Repo:  "nixos",
		Ref:   "agent/4a22ef3b-312b-4624-8e64-9a3060579091/test-slug",
		Title: `change "docs"`,
		Body:  "line one\nline two\n\nwith a blank line",
	}

	line, err := Encode(want)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.HasPrefix(line, Prefix) {
		t.Fatalf("Encode() = %q, want the %q prefix", line, Prefix)
	}
	if strings.Contains(line, "\n") {
		t.Fatalf("Encode() emitted a newline; the trailer must be exactly one line: %q", line)
	}

	// The JSON after the prefix must be valid and compact. A pretty-printed or
	// newline-bearing value would split the trailer across commit-message lines.
	payload := strings.TrimPrefix(line, Prefix)
	if !json.Valid([]byte(payload)) {
		t.Fatalf("metadata payload is not valid JSON: %q", payload)
	}
	if strings.Contains(payload, "\n") {
		t.Fatalf("metadata payload contains a raw newline: %q", payload)
	}
	if !strings.Contains(payload, `\n`) {
		t.Errorf("body newlines were not escaped in %q", payload)
	}

	message := "test: change\n\n" + line + "\n"
	got, ok := Parse(message)
	if !ok {
		t.Fatalf("Parse did not find metadata in %q", message)
	}
	if got != want {
		t.Errorf("Parse() = %+v, want %+v", got, want)
	}
}

// TestParseLastMetadataLineWins covers FR-34's "last matching line" rule. A
// commit message may quote an earlier trailer (for example in the body); the
// validator appends the authoritative one last, so Parse must prefer it.
func TestParseLastMetadataLineWins(t *testing.T) {
	first, err := Encode(Metadata{Repo: "nixos", Ref: "agent/first", Title: "first", Body: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Encode(Metadata{Repo: "nixos", Ref: "agent/second", Title: "second", Body: "second"})
	if err != nil {
		t.Fatal(err)
	}

	message := strings.Join([]string{
		"test: change",
		"",
		"quoted earlier trailer:",
		first,
		"",
		"authoritative trailer:",
		second,
	}, "\n")

	got, ok := Parse(message)
	if !ok {
		t.Fatalf("Parse did not find metadata in %q", message)
	}
	if got.Ref != "agent/second" {
		t.Errorf("Parse chose ref %q, want the last metadata line", got.Ref)
	}
}

// TestParseAbsentOrInvalid covers FR-34's failure path: a missing, malformed,
// or type-mismatched trailer is not metadata. Returning false, rather than a
// zero value with true, is what lets the verifier reject the bundle.
func TestParseAbsentOrInvalid(t *testing.T) {
	tests := []struct {
		name    string
		message string
	}{
		{name: "empty", message: ""},
		{name: "ordinary message", message: "test: change\n\nno trailer here\n"},
		{name: "prefix without JSON", message: "test: change\n\n" + Prefix + "\n"},
		{name: "invalid JSON", message: "test: change\n\n" + Prefix + "{not json}\n"},
		{name: "wrong field type", message: Prefix + `{"repo":1,"ref":"agent/x","title":"t","body":"b"}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := Parse(tc.message); ok {
				t.Fatalf("Parse(%q) = (%+v, true), want false", tc.message, got)
			}
		})
	}
}

// TestParseInvalidLastMetadataLineWinsTheFailure pins the other half of the
// last-line rule: if the final matching line is corrupt, Parse must fail rather
// than silently falling back to an earlier, stale trailer.
func TestParseInvalidLastMetadataLineWinsTheFailure(t *testing.T) {
	valid, err := Encode(Metadata{Repo: "nixos", Ref: "agent/first", Title: "first", Body: "first"})
	if err != nil {
		t.Fatal(err)
	}
	message := valid + "\n" + Prefix + "{not json}"

	if got, ok := Parse(message); ok {
		t.Fatalf("Parse(%q) = (%+v, true), want false when the last metadata line is invalid", message, got)
	}
}
