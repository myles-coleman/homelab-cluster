// Package commitmeta carries the publish metadata inside the validator's commit
// message (spec 30, FR-34).
//
// The publisher receives only {repo, bundle, bundle_sha256, canonical_hash} and
// must re-derive the canonical hash from the bundle. The hash covers repo, ref,
// commit, tree, title and body, so the validator appends one trailer line before
// committing:
//
//	Agent-PR-Metadata: {"repo":"...","ref":"...","title":"...","body":"..."}
//
// JSON is compact, escaped and deterministic; the trailer is therefore always one
// line and can be parsed back without ambiguity.
package commitmeta

import (
	"encoding/json"
	"strings"
)

// Prefix identifies the trailer line.
const Prefix = "Agent-PR-Metadata: "

// Metadata is exactly the set of fields the canonical hash needs that are not
// derivable from the commit object.
type Metadata struct {
	Repo  string `json:"repo"`
	Ref   string `json:"ref"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

// Encode renders the trailer line. The result contains no newline: encoding/json
// escapes every newline in the body.
func Encode(m Metadata) (string, error) {
	payload, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return Prefix + string(payload), nil
}

// Parse finds the last metadata line in a commit message. The last line wins
// because an earlier trailer may be quoted in the body; if that last line is
// corrupt, Parse returns false rather than falling back to stale metadata.
func Parse(message string) (Metadata, bool) {
	last := ""
	found := false
	for _, line := range strings.Split(message, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, Prefix) {
			last = line
			found = true
		}
	}
	if !found {
		return Metadata{}, false
	}

	var m Metadata
	if err := json.Unmarshal([]byte(strings.TrimPrefix(last, Prefix)), &m); err != nil {
		return Metadata{}, false
	}
	return m, true
}
