// Package refname validates the only ref shape the broker may create (spec 30, FR-30):
//
//	agent/<sandbox-uid>/<slug>
//
// where <sandbox-uid> is the Kubernetes UID of the owning Sandbox and <slug>
// matches ^[a-z0-9][a-z0-9-]{0,40}$.
package refname

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrInvalidRef is returned for every rejected ref.
var ErrInvalidRef = errors.New("refname: invalid ref")

// uidPattern accepts the canonical 8-4-4-4-12 lowercase hex UUID form. Any
// version is allowed; the broker only needs the UID to be unambiguous and
// path-safe.
var uidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// slugPattern is the exact grammar pinned by FR-30.
var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)

const prefix = "agent/"

// Build returns the canonical ref for a Sandbox UID and slug.
func Build(sandboxUID, slug string) (string, error) {
	if !isValidUID(sandboxUID) {
		return "", fmt.Errorf("%w: sandbox uid is not a canonical UUID", ErrInvalidRef)
	}
	if !ValidSlug(slug) {
		return "", fmt.Errorf("%w: slug does not match the allowed pattern", ErrInvalidRef)
	}
	return prefix + sandboxUID + "/" + slug, nil
}

// Validate accepts exactly agent/<uid>/<slug> and rejects anything else.
func Validate(ref string) error {
	if ref == "" {
		return fmt.Errorf("%w: empty ref", ErrInvalidRef)
	}

	// Git refuses refs containing control characters or these metacharacters,
	// and a lookalike component could escape the agent/* namespace (FR-30).
	for _, r := range ref {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: ref contains a control character", ErrInvalidRef)
		}
	}
	if strings.ContainsAny(ref, "~^:?*[\\") {
		return fmt.Errorf("%w: ref contains a character git treats specially", ErrInvalidRef)
	}
	// Git also reserves a trailing .lock, case-insensitively.
	if strings.HasSuffix(strings.ToLower(ref), ".lock") {
		return fmt.Errorf("%w: ref ends in .lock", ErrInvalidRef)
	}

	parts := strings.Split(ref, "/")
	if len(parts) != 3 {
		return fmt.Errorf("%w: ref must have exactly three components", ErrInvalidRef)
	}
	if parts[0] != "agent" {
		return fmt.Errorf("%w: ref must start with agent/", ErrInvalidRef)
	}

	for _, part := range parts {
		if part == "" {
			return fmt.Errorf("%w: ref has an empty component", ErrInvalidRef)
		}
		if part == "." || part == ".." {
			return fmt.Errorf("%w: ref has a traversal component", ErrInvalidRef)
		}
	}
	if !isValidUID(parts[1]) {
		return fmt.Errorf("%w: sandbox uid is not a canonical UUID", ErrInvalidRef)
	}
	if !ValidSlug(parts[2]) {
		return fmt.Errorf("%w: slug does not match the allowed pattern", ErrInvalidRef)
	}

	return nil
}

// ValidSlug reports whether slug matches the allowed pattern.
func ValidSlug(slug string) bool {
	return slugPattern.MatchString(slug)
}

func isValidUID(uid string) bool {
	return uidPattern.MatchString(uid)
}
