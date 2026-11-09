package policy

import (
	"reflect"
	"testing"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/rules"
	policyfs "github.com/myles-coleman/homelab-cluster/services/agent-broker/policy"
)

// mustEmbedded loads the shipped allowlist. Every behavioural test below runs
// against the real file rather than a fixture, because the point is that the
// deployed policy is the one that denies (FR-20, FR-21).
func mustEmbedded(t *testing.T) *Policy {
	t.Helper()
	p, err := Embedded()
	if err != nil {
		t.Fatalf("Embedded(): %v", err)
	}
	if p == nil {
		t.Fatal("Embedded() returned a nil policy")
	}
	return p
}

// wantRejection asserts a structured denial. Rule ids are API surface, so a
// denial that is not a *rules.Rejection is a contract failure even though the
// path was refused.
func wantRejection(t *testing.T, p *Policy, repo, path, wantRule string) {
	t.Helper()
	err := p.Check(repo, path)
	if err == nil {
		t.Fatalf("Check(%q, %q) = nil, want rejection %q", repo, path, wantRule)
	}
	rej, ok := rules.From(err)
	if !ok {
		t.Fatalf("Check(%q, %q) = %v, want a *rules.Rejection", repo, path, err)
	}
	if rej.RuleID != wantRule {
		t.Errorf("Check(%q, %q) rule = %q, want %q", repo, path, rej.RuleID, wantRule)
	}
	// A rejection reason is shown to the agent, so it must say something.
	if rej.Reason == "" {
		t.Errorf("Check(%q, %q) returned an empty reason", repo, path)
	}
}

func wantAllowed(t *testing.T, p *Policy, repo, path string) {
	t.Helper()
	if err := p.Check(repo, path); err != nil {
		t.Fatalf("Check(%q, %q) = %v, want nil", repo, path, err)
	}
}

// TestEmbeddedMatchesShippedFile pins FR-32's single-source-of-truth claim: the
// embedded copy and the file served at GET /v1/policy must parse identically, so
// the automated path can never be narrower than the documented one.
func TestEmbeddedMatchesShippedFile(t *testing.T) {
	embedded := mustEmbedded(t)

	parsed, err := Parse(policyfs.YAML)
	if err != nil {
		t.Fatalf("Parse(policyfs.YAML): %v", err)
	}
	if !reflect.DeepEqual(embedded, parsed) {
		t.Errorf("Embedded() and Parse(policyfs.YAML) disagree:\n embedded = %#v\n parsed   = %#v", embedded, parsed)
	}
}

func TestEmbeddedShape(t *testing.T) {
	p := mustEmbedded(t)

	if p.Default != "deny" {
		t.Errorf("Default = %q, want %q", p.Default, "deny")
	}
	for _, repo := range []string{"nixos", "homelab-cluster"} {
		r, ok := p.Repos[repo]
		if !ok {
			t.Errorf("policy has no entry for repo %q", repo)
			continue
		}
		if len(r.Allow) == 0 {
			t.Errorf("repo %q has an empty allow list", repo)
		}
		if len(r.Deny) == 0 {
			t.Errorf("repo %q has an empty deny list", repo)
		}
	}
}

// TestUnknownRepo covers FR-20's "unknown repositories are rejected outright".
func TestUnknownRepo(t *testing.T) {
	p := mustEmbedded(t)

	for _, repo := range []string{"", "dotfiles", "nixos.git", "Nixos", "homelab", "agent-broker"} {
		t.Run(repo, func(t *testing.T) {
			wantRejection(t, p, repo, "docs/x.md", rules.UnknownRepo)
		})
	}
}

// TestDefaultDeny covers FR-20's default-deny rule: a changed path that matches
// no allow glob is refused, including paths that look harmless.
func TestDefaultDeny(t *testing.T) {
	p := mustEmbedded(t)

	tests := []struct{ repo, path string }{
		{"nixos", "random.txt"},
		{"nixos", "modulesx/foo.nix"},
		{"homelab-cluster", "README.md"},
		{"homelab-cluster", "modules/foo.nix"},
		{"homelab-cluster", "manifests/clusterx/foo.yaml"},
	}
	for _, tc := range tests {
		t.Run(tc.repo+":"+tc.path, func(t *testing.T) {
			wantRejection(t, p, tc.repo, tc.path, rules.PathNotAllowed)
		})
	}
}

// TestDenyBeatsAllow covers FR-20's precedence rule. Each path here matches an
// allow glob and a deny glob; only the deny may win.
func TestDenyBeatsAllow(t *testing.T) {
	p := mustEmbedded(t)

	tests := []struct{ repo, path string }{
		// modules/** allows it; modules/sops.nix denies it.
		{"nixos", "modules/sops.nix"},
		// modules/** allows it; modules/aspects/k3s-*.nix denies it.
		{"nixos", "modules/aspects/k3s-node.nix"},
		// manifests/cluster/** allows it; the exact file is denied.
		{"homelab-cluster", "manifests/cluster/kustomization.yaml"},
		// manifests/cluster/** allows it; the subtree is denied.
		{"homelab-cluster", "manifests/cluster/cert-manager/deployment.yaml"},
	}
	for _, tc := range tests {
		t.Run(tc.repo+":"+tc.path, func(t *testing.T) {
			wantRejection(t, p, tc.repo, tc.path, rules.PathNotAllowed)
		})
	}
}

// TestMatchGlobSemantics covers the pattern language in the package doc: `**`
// crosses segments, `*` does not, `?` is exactly one character, and patterns are
// anchored at both ends.
func TestMatchGlobSemantics(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		path    string
		want    bool
	}{
		{"literal exact", "README.md", "README.md", true},
		{"literal is anchored at the end", "README.md", "README.md.bak", false},
		{"literal is anchored at the start", "README.md", "docs/README.md", false},

		{"star within one segment", "modules/*", "modules/a.nix", true},
		{"star does not cross a segment", "modules/*", "modules/a/b.nix", false},
		{"star alone still crosses nothing", "*.nix", "a/b.nix", false},

		{"doublestar crosses segments", "docs/**", "docs/a/b/c.md", true},
		{"doublestar matches direct children", "docs/**", "docs/x.md", true},
		{"doublestar is anchored", "docs/**", "docsx/a", false},
		{"doublestar is anchored on the left", "docs/**", "/docs/a", false},
		{"doublestar with suffix crosses segments", "manifests/cluster/**", "manifests/cluster/a/b/c.yaml", true},

		{"leading doublestar matches deep path", "**/hardware-configuration.nix", "hosts/bee/hardware-configuration.nix", true},
		{"leading doublestar matches zero segments", "**/hardware-configuration.nix", "hardware-configuration.nix", true},
		{"leading doublestar needs the final segment", "**/hardware-configuration.nix", "hosts/bee/configuration.nix", false},

		{"question mark is one character", "docs/?.md", "docs/a.md", true},
		{"question mark rejects two characters", "docs/?.md", "docs/ab.md", false},
		{"question mark rejects zero characters", "docs/?.md", "docs/.md", false},
		{"question mark does not match a slash", "docs/?.md", "docs//.md", false},

		{"empty pattern never matches", "", "docs/x.md", false},
		{"matching is case sensitive", "README.md", "readme.md", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(tc.pattern, tc.path); got != tc.want {
				t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
			}
		})
	}
}

// TestMandatoryDenials enumerates every path FR-21 says must never be
// agent-editable. Each case is named for the path so a regression identifies the
// exact denial that was dropped.
func TestMandatoryDenials(t *testing.T) {
	p := mustEmbedded(t)

	// These paths are dangerous in any repository: build entrypoints, CI
	// workflows, secret material, scripts, images and the privileged module and
	// host subtrees.
	nixosPaths := []string{
		"flake.nix",
		"flake.lock",
		"rebuild.sh",
		".github/workflows/pr.yaml",
		".sops.yaml",
		"secrets/x",
		"scripts/x.sh",
		"images/opencode-sandbox/Dockerfile",
		"modules/sops.nix",
		"modules/aspects/k3s-node.nix",
		"modules/aspects/tailscale.nix",
		"hosts/homelab/default.nix",
		"hosts/homelab/modules/foo.nix",
		"hosts/bee/hardware-configuration.nix",
	}
	homelabPaths := []string{
		"manifests/cluster/kustomization.yaml",
		"manifests/bootstrap/x-app.yaml",
		"generate-apps.sh",
		"manifests/cluster/cert-manager/deployment.yaml",
		"manifests/cluster/external-secrets/secret.yaml",
		"manifests/cluster/traefik/helm-chart.yaml",
		"manifests/cluster/argocd-secret/secret.yaml",
		"manifests/cluster/squid/deployment.yaml",
		"manifests/cluster/sandbox-telemetry/deployment.yaml",
		"manifests/cluster/agent-sandbox/rbac.yaml",
	}

	// The dangerous-path list applies to both repositories: the homelab repo
	// must not gain an allow glob that lets one of these through either.
	for _, repo := range []string{"nixos", "homelab-cluster"} {
		for _, path := range nixosPaths {
			t.Run(repo+"/"+path, func(t *testing.T) {
				wantRejection(t, p, repo, path, rules.PathNotAllowed)
			})
		}
	}
	for _, path := range homelabPaths {
		t.Run("homelab-cluster/"+path, func(t *testing.T) {
			wantRejection(t, p, "homelab-cluster", path, rules.PathNotAllowed)
		})
	}
}

// TestAllowedSurface is the positive control: an over-eager deny list that blocks
// the agent's actual job is a bug too (FR-20).
func TestAllowedSurface(t *testing.T) {
	p := mustEmbedded(t)

	tests := []struct{ repo, path string }{
		{"nixos", "README.md"},
		{"nixos", "modules/foo.nix"},
		{"nixos", "modules/aspects/new-aspect.nix"},
		{"nixos", "docs/x.md"},
		{"nixos", "docs/a/b.md"},
		{"nixos", "hosts/bee/configuration.nix"},
		{"homelab-cluster", "docs/x.md"},
		{"homelab-cluster", "manifests/cluster/newsvc/deployment.yaml"},
		{"homelab-cluster", "manifests/cluster/newsvc/namespace.yaml"},
	}
	for _, tc := range tests {
		t.Run(tc.repo+":"+tc.path, func(t *testing.T) {
			wantAllowed(t, p, tc.repo, tc.path)
		})
	}
}
