// Package policy evaluates the per-repository allowlist (spec 30, FR-20, FR-21).
//
// Semantics, in order:
//
//  1. Unknown repositories are rejected.
//  2. Default deny: a path must match at least one allow glob.
//  3. Deny wins: a path matching any deny glob is rejected even if allowed.
//
// Glob syntax is repository-path oriented: `*` matches within one segment, `**`
// matches any number of segments, `?` matches one character, and patterns are
// anchored at both ends.
package policy

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/rules"
	policyfs "github.com/myles-coleman/homelab-cluster/services/agent-broker/policy"
	"gopkg.in/yaml.v3"
)

// Repo is one repository's glob lists.
type Repo struct {
	Allow []string `yaml:"allow"`
	Deny  []string `yaml:"deny"`
}

// Policy is the parsed allowlist.
type Policy struct {
	Default string          `yaml:"default"`
	Repos   map[string]Repo `yaml:"repos"`
}

// Parse validates and returns a Policy from YAML bytes.
func Parse(data []byte) (*Policy, error) {
	var p Policy
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("policy: decode: %w", err)
	}

	// The only supported default is deny. A missing or misspelled value would
	// otherwise silently weaken the allowlist (FR-20).
	if p.Default == "" {
		return nil, errors.New("policy: missing default")
	}
	if p.Default != "deny" {
		return nil, fmt.Errorf("policy: unknown default %q", p.Default)
	}

	// An allow list is the positive grant; a repo without one can never permit
	// anything and is a configuration error rather than an empty policy (FR-20).
	for name, repo := range p.Repos {
		if len(repo.Allow) == 0 {
			return nil, fmt.Errorf("policy: repo %q has no allow list", name)
		}
	}

	return &p, nil
}

// Embedded parses the compiled-in allowlist.
func Embedded() (*Policy, error) {
	return Parse(policyfs.YAML)
}

// Check returns nil when repo/path is permitted. Otherwise it returns a
// *rules.Rejection carrying rules.UnknownRepo or rules.PathNotAllowed.
func (p *Policy) Check(repo, path string) error {
	if p == nil {
		return rules.New(rules.UnknownRepo, "repository is not in the allowlist")
	}
	entry, ok := p.Repos[repo]
	if !ok {
		return rules.New(rules.UnknownRepo, "repository is not in the allowlist")
	}

	clean := pathpkgClean(path)
	if escapesRoot(clean) {
		return rules.New(rules.PathNotAllowed, "path is not permitted by the repository policy")
	}

	// Deny wins over allow, including when both match (FR-20).
	for _, pattern := range entry.Deny {
		if Match(pattern, clean) {
			return rules.New(rules.PathNotAllowed, "path is not permitted by the repository policy")
		}
	}
	for _, pattern := range entry.Allow {
		if Match(pattern, clean) {
			return nil
		}
	}
	return rules.New(rules.PathNotAllowed, "path is not permitted by the repository policy")
}

// pathpkgClean is path.Clean, kept behind a name so the policy rule ("clean
// repository-relative paths") reads as a decision rather than an implementation
// detail.
func pathpkgClean(name string) string { return path.Clean(name) }

// escapesRoot reports whether a cleaned path climbs out of the repository.
func escapesRoot(name string) bool {
	return name == ".." || strings.HasPrefix(name, "../")
}

// Match reports whether a repository-relative path matches one glob.
func Match(pattern, name string) bool {
	if pattern == "" || name == "" {
		return false
	}

	// Patterns are anchored against the cleaned repository-relative path, and a
	// path that escapes the root never matches a repository glob.
	clean := pathpkgClean(name)
	if escapesRoot(clean) {
		return false
	}

	return matchGlob([]rune(pattern), []rune(clean))
}

// matchGlob is a small, dependency-free glob matcher.
//
// `**` crosses path segments; a `**/` prefix may also match zero segments, so
// `**/x` matches `x`. A single `*` stops at `/`, and `?` matches exactly one
// non-slash character. Both pattern and name are anchored.
func matchGlob(pattern, name []rune) bool {
	for len(pattern) > 0 {
		switch pattern[0] {
		case '*':
			if len(pattern) >= 2 && pattern[1] == '*' {
				rest := pattern[2:]

				// Treat `**/` as "zero or more leading segments" so a pattern
				// like `**/hardware-configuration.nix` also matches a file at
				// the repository root.
				if len(rest) > 0 && rest[0] == '/' {
					tail := rest[1:]
					if matchGlob(tail, name) {
						return true
					}
					for i := 0; i < len(name); i++ {
						if name[i] == '/' && matchGlob(tail, name[i+1:]) {
							return true
						}
					}
					return false
				}

				// A bare `**` may consume any characters, including slashes.
				for i := 0; i <= len(name); i++ {
					if matchGlob(rest, name[i:]) {
						return true
					}
				}
				return false
			}

			// A single `*` matches within one segment only.
			rest := pattern[1:]
			for i := 0; i <= len(name); i++ {
				if matchGlob(rest, name[i:]) {
					return true
				}
				if i == len(name) || name[i] == '/' {
					break
				}
			}
			return false

		case '?':
			if len(name) == 0 || name[0] == '/' {
				return false
			}
			pattern, name = pattern[1:], name[1:]

		default:
			if len(name) == 0 || name[0] != pattern[0] {
				return false
			}
			pattern, name = pattern[1:], name[1:]
		}
	}

	return len(name) == 0
}
