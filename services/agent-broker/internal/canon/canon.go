// Package canon computes the two hashes the broker returns (spec 30, FR-28, FR-29).
//
// canonical_hash = SHA-256 over a canonical JSON encoding of
// {repo, ref, commit_sha, tree_sha, title, body}: keys sorted, no insignificant
// whitespace, UTF-8, body newline-normalised. bundle_sha256 is a separate
// integrity hash over the bundle bytes.
package canon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// CanonicalJSON returns the canonical encoding used for the hash.
func CanonicalJSON(repo, ref, commitSHA, treeSHA, title, body string) ([]byte, error) {
	// encoding/json emits map keys in sorted order and uses compact separators,
	// which is exactly the canonical form FR-29 pins. The map literal also makes
	// adding a field a deliberate change: the key set is the hash's input.
	fields := map[string]string{
		"repo":       repo,
		"ref":        ref,
		"commit_sha": commitSHA,
		"tree_sha":   treeSHA,
		"title":      title,
		"body":       normalizeNewlines(body),
	}
	return json.Marshal(fields)
}

// Hash returns the hex SHA-256 of CanonicalJSON.
func Hash(repo, ref, commitSHA, treeSHA, title, body string) (string, error) {
	canonical, err := CanonicalJSON(repo, ref, commitSHA, treeSHA, title, body)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// BundleSHA256 returns the hex SHA-256 of the bundle bytes.
func BundleSHA256(bundle []byte) string {
	sum := sha256.Sum256(bundle)
	return hex.EncodeToString(sum[:])
}

// normalizeNewlines converts CRLF and lone CR to LF so the same text cannot
// hash two ways (FR-29). Nothing else is trimmed: a trailing newline is content.
func normalizeNewlines(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}
