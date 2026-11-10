// Package publish: RenderBody renders the draft PR body (spec 30, FR-50).
package publish

import (
	"fmt"
	"strings"
)

// RenderBody builds the four-section body in the order the contract pins.
func RenderBody(v Verified) string {
	var b strings.Builder

	b.WriteString("## Canonical hash\n\n")
	b.WriteString("`" + v.CanonicalHash + "`\n\n")

	b.WriteString("## Validation\n\n")
	b.WriteString("- bundle_sha256 verified against the submitted bytes\n")
	b.WriteString("- canonical hash re-derived by the publisher from the commit and metadata\n")
	b.WriteString("- single non-merge commit on base `" + v.BaseCommit + "`\n")
	fmt.Fprintf(&b, "- %d changed paths\n", len(v.Files))
	b.WriteString("- validator policy and secret-scan checks passed\n\n")

	b.WriteString("## Changed paths\n\n")
	for _, change := range v.Files {
		b.WriteString("- " + change.Status + " " + change.Path + "\n")
	}
	if len(v.Files) == 0 {
		b.WriteString("- none\n")
	}
	b.WriteString("\n")

	b.WriteString("## Blast radius\n\n")
	b.WriteString(blastRadius(v.Repo))
	b.WriteString("\n")

	return b.String()
}

func blastRadius(repo string) string {
	switch repo {
	case "homelab-cluster":
		return "merging triggers ArgoCD reconciliation of the cluster, including `homelab`, which grants effective root on the host that runs the sandbox node."
	case "nixos":
		return "merging triggers the deploy-rs deployment for `homelab` and `bee-gpu-server`."
	default:
		return "merging applies the change to the served repository; review the changed paths above."
	}
}
