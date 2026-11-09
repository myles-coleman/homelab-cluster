// Package scan detects secret-shaped content and encoded payloads in a submitted
// change (spec 30, FR-25, FR-26).
//
// It runs fully offline: no network package is imported anywhere in this package,
// and verification against third-party providers is explicitly out of scope.
//
// Every finding names a location (repository path or metadata field) and never
// the offending value, so the error channel cannot become a leak.
package scan

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// maxDecodedInput bounds decoder work so a hostile submission cannot make the
// validator spend unbounded effort unwrapping encodings (FR-26).
const maxDecodedInput = 1 << 20 // 1 MiB

// Finding is one detected problem.
type Finding struct {
	// Location is a repository path, or one of "title", "body", "commit_message".
	Location string
	// Reason is a short, value-free description such as "private key block".
	Reason string
}

// Scanner is stateless and safe for concurrent use.
type Scanner struct{}

// New returns a Scanner.
func New() *Scanner { return &Scanner{} }

type detector struct {
	reason string
	re     *regexp.Regexp
}

// detectors is the plaintext shape set from FR-25. Regexes are compiled once and
// are safe for concurrent use; reasons deliberately never include a match.
var detectors = []detector{
	// PEM private keys, including the algorithm-qualified form.
	{reason: "private key block", re: regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)},
	// GitHub personal access and app tokens. Real tokens carry 36 characters
	// after the prefix, but older and test fixtures are a little shorter, so the
	// floor is set just below that to keep them detectable.
	{reason: "GitHub token", re: regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{30,}`)},
	// AWS access key ids.
	{reason: "AWS access key", re: regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	// JSON Web Tokens: header.payload.signature, each base64url-ish.
	{reason: "JSON Web Token", re: regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`)},
	// OpenSSH authorized_keys entries.
	{reason: "SSH public key", re: regexp.MustCompile(`ssh-(?:ed25519|rsa|dss) AAAA`)},
	// `password = "..."` and friends. The value must be long enough to be a
	// credential, which keeps ordinary prose such as "token bucket" quiet.
	{reason: "credential assignment", re: regexp.MustCompile(`(?i)(password|passwd|secret|token|api[_-]?key)\s*[:=]\s*["']?[A-Za-z0-9/+_.-]{12,}`)},
}

// Text scans arbitrary text and returns findings. Decoding is attempted before
// matching can be missed: base64, hex, percent-encoding and unicode escapes are
// unwrapped, so a wrapped secret is still found.
func (s *Scanner) Text(location string, data []byte) []Finding {
	seen := make(map[string]bool)
	var findings []Finding

	add := func(text string) {
		for _, d := range detectors {
			if !d.re.MatchString(text) || seen[d.reason] {
				continue
			}
			seen[d.reason] = true
			findings = append(findings, Finding{Location: location, Reason: d.reason})
		}
	}

	// Always match the text as submitted.
	add(string(data))

	// Then try every supported wrapper. A decoded payload that is mostly binary
	// noise is skipped: it cannot contain a readable credential and matching it
	// would only invite false positives.
	for _, decoded := range decodeAll(data) {
		if len(decoded) == 0 || !mostlyPrintable(decoded) {
			continue
		}
		add(string(decoded))
	}

	return findings
}

// decodeAll returns the candidate decoded forms of data. It never recurses:
// one layer of wrapping is what the manual collector documents (FR-26), and
// recursion would let a crafted input amplify work.
func decodeAll(data []byte) [][]byte {
	if len(data) > maxDecodedInput {
		data = data[:maxDecodedInput]
	}
	s := string(data)

	var out [][]byte
	if b, ok := decodeBase64(s); ok {
		out = append(out, b)
	}
	if b, ok := decodeHex(s); ok {
		out = append(out, b)
	}
	if b, ok := decodePercent(s); ok {
		out = append(out, b)
	}
	if b, ok := decodeUnicodeEscapes(s); ok {
		out = append(out, b)
	}

	for i, b := range out {
		if len(b) > maxDecodedInput {
			out[i] = b[:maxDecodedInput]
		}
	}
	return out
}

// decodeBase64 tries the standard and raw (unpadded) alphabets, both of which
// appear in hand-written configuration.
func decodeBase64(s string) ([]byte, bool) {
	trimmed := strings.TrimSpace(s)
	if len(trimmed) < 16 {
		return nil, false
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		if b, err := enc.DecodeString(trimmed); err == nil && len(b) > 0 {
			return b, true
		}
	}
	return nil, false
}

// decodeHex decodes a whole input that is an even-length hex string. Requiring a
// minimum length keeps short hex-looking words from being treated as payloads.
func decodeHex(s string) ([]byte, bool) {
	trimmed := strings.TrimSpace(s)
	if len(trimmed) < 16 || len(trimmed)%2 != 0 {
		return nil, false
	}
	b, err := hex.DecodeString(trimmed)
	if err != nil {
		return nil, false
	}
	return b, true
}

// decodePercent expands %XX escapes. A plus sign is left alone because it is a
// legitimate credential character, unlike in form encoding.
func decodePercent(s string) ([]byte, bool) {
	if !strings.Contains(s, "%") {
		return nil, false
	}
	out := make([]byte, 0, len(s))
	changed := false
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if v, ok := hexByte(s[i+1], s[i+2]); ok {
				out = append(out, v)
				i += 2
				changed = true
				continue
			}
		}
		out = append(out, s[i])
	}
	if !changed {
		return nil, false
	}
	return out, true
}

// decodeUnicodeEscapes expands \uXXXX and \UXXXXXXXX escapes, including surrogate
// pairs, which is how a JSON or Nix string can smuggle a credential.
func decodeUnicodeEscapes(s string) ([]byte, bool) {
	if !strings.Contains(s, `\u`) && !strings.Contains(s, `\U`) {
		return nil, false
	}

	var buf bytes.Buffer
	buf.Grow(len(s))
	changed := false

	for i := 0; i < len(s); {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'u':
				if i+6 <= len(s) {
					if v, ok := hex4(s[i+2 : i+6]); ok {
						r := rune(v)
						i += 6
						if utf16.IsSurrogate(r) && i+6 <= len(s) && s[i] == '\\' && s[i+1] == 'u' {
							if v2, ok := hex4(s[i+2 : i+6]); ok {
								if dec := utf16.DecodeRune(r, rune(v2)); dec != utf8.RuneError {
									buf.WriteRune(dec)
									i += 6
									changed = true
									continue
								}
							}
						}
						if utf16.IsSurrogate(r) {
							buf.WriteRune(utf8.RuneError)
						} else {
							buf.WriteRune(r)
						}
						changed = true
						continue
					}
				}
			case 'U':
				if i+10 <= len(s) {
					if v, ok := hex8(s[i+2 : i+10]); ok && v <= utf8.MaxRune && !utf16.IsSurrogate(rune(v)) {
						buf.WriteRune(rune(v))
						i += 10
						changed = true
						continue
					}
				}
			}
		}
		buf.WriteByte(s[i])
		i++
	}

	if !changed {
		return nil, false
	}
	return buf.Bytes(), true
}

// mostlyPrintable reports whether a decoded payload is text rather than binary
// noise. The threshold is generous because the decoded text may contain PEM
// headers, whitespace and punctuation.
func mostlyPrintable(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	printable, total := 0, 0
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		b = b[size:]
		total++
		if r == utf8.RuneError && size == 1 {
			continue // invalid UTF-8
		}
		if r == '\n' || r == '\r' || r == '\t' || (r >= 0x20 && r != 0x7f) {
			printable++
		}
	}
	return printable*100 >= total*90
}

func hexByte(hi, lo byte) (byte, bool) {
	h, ok := hexVal(hi)
	if !ok {
		return 0, false
	}
	l, ok := hexVal(lo)
	if !ok {
		return 0, false
	}
	return h<<4 | l, true
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

func hex4(s string) (uint16, bool) {
	if len(s) != 4 {
		return 0, false
	}
	var v uint16
	for i := 0; i < 4; i++ {
		d, ok := hexVal(s[i])
		if !ok {
			return 0, false
		}
		v = v<<4 | uint16(d)
	}
	return v, true
}

func hex8(s string) (uint32, bool) {
	if len(s) != 8 {
		return 0, false
	}
	var v uint32
	for i := 0; i < 8; i++ {
		d, ok := hexVal(s[i])
		if !ok {
			return 0, false
		}
		v = v<<4 | uint32(d)
	}
	return v, true
}
