// Package policyfs embeds the canonical allowlist into the validator image.
//
// The same file is served at GET /v1/policy and read by the manual collector, so
// there is exactly one allowlist in the system (FR-32).
package policyfs

import _ "embed"

// YAML is the contents of policy/repos.yaml.
//
//go:embed repos.yaml
var YAML []byte
