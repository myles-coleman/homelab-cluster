package refname

import (
	"errors"
	"strings"
	"testing"
)

const validUID = "6fa8e2f0-1c3d-4a5b-8c7d-9e0f1a2b3c4d"

// TestValidateAccepts pins the only ref shape the broker may create (FR-30).
func TestValidateAccepts(t *testing.T) {
	tests := []struct{ name, ref string }{
		{"canonical", "agent/" + validUID + "/my-slug"},
		{"one character slug", "agent/" + validUID + "/a"},
		{"slug at the 41 character limit", "agent/" + validUID + "/" + strings.Repeat("a", 41)},
		{"digits and hyphens", "agent/" + validUID + "/fix-1-thing-2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := Validate(tc.ref); err != nil {
				t.Fatalf("Validate(%q) = %v, want nil", tc.ref, err)
			}
		})
	}
}

// TestValidateRejects covers FR-30's rejection list. Each case is a ref that a
// permissive validator (or git's own ref rules) might allow, and each is a way
// an agent could try to escape the agent/* namespace or write a ref other
// tooling treats specially.
func TestValidateRejects(t *testing.T) {
	tests := []struct{ name, ref string }{
		{"empty", ""},
		{"missing prefix", validUID + "/my-slug"},
		{"wrong prefix", "refs/agent/" + validUID + "/my-slug"},
		{"prefix lookalike", "agentx/" + validUID + "/my-slug"},
		{"empty uid", "agent//my-slug"},
		{"non-uuid uid", "agent/not-a-uuid/my-slug"},
		{"uuid-shaped but non-hex uid", "agent/zzzzzzzz-1c3d-4a5b-8c7d-9e0f1a2b3c4d/my-slug"},
		{"uuid with extra suffix", "agent/" + validUID + "-extra/my-slug"},
		{"uppercase slug", "agent/" + validUID + "/My-Slug"},
		{"slug starts with a hyphen", "agent/" + validUID + "/-leading"},
		{"slug is dot dot", "agent/" + validUID + "/.."},
		{"path traversal in uid", "agent/../../etc"},
		{"embedded slash in slug", "agent/" + validUID + "/a/b"},
		{"extra trailing segment", "agent/" + validUID + "/my-slug/extra"},
		{"trailing slash", "agent/" + validUID + "/"},
		{"missing slug", "agent/" + validUID},
		{"slug longer than 41 characters", "agent/" + validUID + "/" + strings.Repeat("a", 42)},

		// Characters git or other ref consumers treat specially (FR-30).
		{"tilde", "agent/" + validUID + "/sl~ug"},
		{"caret", "agent/" + validUID + "/sl^ug"},
		{"colon", "agent/" + validUID + "/sl:ug"},
		{"question mark", "agent/" + validUID + "/sl?ug"},
		{"asterisk", "agent/" + validUID + "/sl*ug"},
		{"open bracket", "agent/" + validUID + "/sl[ug"},
		{"backslash", "agent/" + validUID + "/sl\\ug"},
		{"nul byte", "agent/" + validUID + "/slug\x00"},
		{"control character", "agent/" + validUID + "/sl\x01ug"},
		{"delete character", "agent/" + validUID + "/slug\x7f"},
		{"trailing dot lock", "agent/" + validUID + "/slug.lock"},
		{"leading dot", "agent/" + validUID + "/.hidden"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.ref)
			if err == nil {
				t.Fatalf("Validate(%q) = nil, want a rejection", tc.ref)
			}
			// ErrInvalidRef is documented as the sentinel for every rejected ref,
			// so callers can branch on it without parsing messages.
			if !errors.Is(err, ErrInvalidRef) {
				t.Errorf("Validate(%q) = %v, want an error wrapping ErrInvalidRef", tc.ref, err)
			}
		})
	}
}

func TestValidSlug(t *testing.T) {
	tests := []struct {
		name string
		slug string
		want bool
	}{
		{"one character", "a", true},
		{"typical", "my-slug", true},
		{"digits", "123", true},
		{"at the 41 character limit", strings.Repeat("a", 41), true},
		{"over the 41 character limit", strings.Repeat("a", 42), false},
		{"empty", "", false},
		{"uppercase", "My-Slug", false},
		{"starts with hyphen", "-slug", false},
		{"ends with hyphen", "slug-", true}, // the pattern only constrains the first character
		{"underscore", "my_slug", false},
		{"slash", "my/slug", false},
		{"dot", "my.slug", false},
		{"space", "my slug", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidSlug(tc.slug); got != tc.want {
				t.Errorf("ValidSlug(%q) = %v, want %v", tc.slug, got, tc.want)
			}
		})
	}
}

// TestBuildRoundTrips covers FR-30 end to end: whatever Build produces must be
// accepted by Validate, otherwise the broker can emit refs it later refuses.
func TestBuildRoundTrips(t *testing.T) {
	slugs := []string{
		"a",
		"my-slug",
		"fix-1-thing-2",
		strings.Repeat("a", 41),
	}
	for _, slug := range slugs {
		t.Run(slugPrefix(slug, 12), func(t *testing.T) {
			ref, err := Build(validUID, slug)
			if err != nil {
				t.Fatalf("Build(%q, %q) = %v", validUID, slug, err)
			}
			want := "agent/" + validUID + "/" + slug
			if ref != want {
				t.Errorf("Build(%q, %q) = %q, want %q", validUID, slug, ref, want)
			}
			if err := Validate(ref); err != nil {
				t.Errorf("Validate(Build(%q, %q)) = %v, want nil", validUID, slug, err)
			}
		})
	}
}

func TestBuildRejectsInvalidParts(t *testing.T) {
	tests := []struct{ name, uid, slug string }{
		{"invalid slug", validUID, "-bad"},
		{"empty slug", validUID, ""},
		{"uppercase slug", validUID, "Bad"},
		{"invalid uid", "not-a-uuid", "my-slug"},
		{"empty uid", "", "my-slug"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := Build(tc.uid, tc.slug)
			if err == nil {
				t.Fatalf("Build(%q, %q) = %q, want an error", tc.uid, tc.slug, ref)
			}
			if !errors.Is(err, ErrInvalidRef) {
				t.Errorf("Build(%q, %q) = %v, want an error wrapping ErrInvalidRef", tc.uid, tc.slug, err)
			}
			if ref != "" {
				t.Errorf("Build(%q, %q) = %q on error, want empty", tc.uid, tc.slug, ref)
			}
		})
	}
}

// slugPrefix keeps subtest names readable when the slug is a long repeat.
func slugPrefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
