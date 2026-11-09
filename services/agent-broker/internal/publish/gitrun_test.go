package publish

import (
	"encoding/base64"
	"strings"
	"testing"
)

// TestRedactGitStderr covers FR-40's "never leak the token" rule on the error
// path. git only ever receives the token through GIT_CONFIG_*, but a debug
// environment (GIT_TRACE_CURL / GIT_CURL_VERBOSE) can copy the Authorization
// header into stderr, which the publisher then logs. Redaction must remove both
// the encoded credential and the raw token.
func TestRedactGitStderr(t *testing.T) {
	const token = "ghs_supersecret_token_value"
	encoded := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))

	input := "fatal: unable to access 'https://github.com/myles-coleman/nixos.git/'\n" +
		"> Authorization: Basic " + encoded + "\n" +
		"> x-access-token:" + token + "\n" +
		"error: failed to push some refs\n"

	got := redactGitStderr(input)
	if strings.Contains(got, token) {
		t.Errorf("redacted stderr still contains the raw token:\n%s", got)
	}
	if strings.Contains(got, encoded) {
		t.Errorf("redacted stderr still contains the encoded credential:\n%s", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Errorf("redacted stderr has no [REDACTED] marker:\n%s", got)
	}
	// Non-secret diagnostics must survive so an operator can still debug.
	if !strings.Contains(got, "failed to push some refs") {
		t.Errorf("redaction removed useful diagnostics:\n%s", got)
	}
}

// TestRedactGitStderrNoop keeps the redactor from mangling ordinary output.
func TestRedactGitStderrNoop(t *testing.T) {
	input := "fatal: repository not found\n"
	if got := redactGitStderr(input); got != input {
		t.Errorf("redactGitStderr changed secret-free input: %q -> %q", input, got)
	}
}
