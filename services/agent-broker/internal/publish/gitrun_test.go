package publish

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
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

// TestRunGitRedactsStderrOnFailure proves the redactor is wired into the error
// path, not merely available. A fake git copies an Authorization header to
// stderr and exits non-zero; the error the publisher logs must not carry the
// credential. Without the call-site redaction this test fails.
func TestRunGitRedactsStderrOnFailure(t *testing.T) {
	const token = "ghs_supersecret_token_value"
	encoded := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))

	dir := t.TempDir()
	bin := filepath.Join(dir, "git")
	script := "#!/bin/sh\nprintf 'Authorization: Basic %s\\n' \"$CRED\" >&2\nprintf 'x-access-token:%s\\n' \"$TOK\" >&2\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("CRED", encoded)
	t.Setenv("TOK", token)

	_, err := runGit(context.Background(), bin, dir, nil, "push")
	if err == nil {
		t.Fatal("runGit returned nil despite a non-zero git exit")
	}
	msg := err.Error()
	if strings.Contains(msg, token) {
		t.Errorf("runGit error contains the raw token: %s", msg)
	}
	if strings.Contains(msg, encoded) {
		t.Errorf("runGit error contains the encoded credential: %s", msg)
	}
	if !strings.Contains(msg, "[REDACTED]") {
		t.Errorf("runGit error contains no redaction marker: %s", msg)
	}
}
