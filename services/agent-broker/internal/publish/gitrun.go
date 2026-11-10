// Package publish: small git process helpers shared by the verifier and pusher.
package publish

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// gitAuthorizationHeader matches "Authorization: Basic <value>" and
// "Authorization: Bearer <value>", which a debug environment can copy into git
// stderr via GIT_TRACE_CURL or GIT_CURL_VERBOSE.
var gitAuthorizationHeader = regexp.MustCompile(`(?i)(Authorization:\s*(?:Basic|Bearer)\s+)\S+`)

// gitAccessToken matches the raw "x-access-token:<value>" credential form.
var gitAccessToken = regexp.MustCompile(`(?i)(x-access-token:)\S*`)

// redactGitStderr removes credentials from git stderr while preserving the rest
// of the diagnostic, so the publisher can log a failure without leaking the
// installation token (FR-40).
func redactGitStderr(s string) string {
	s = gitAuthorizationHeader.ReplaceAllString(s, "${1}[REDACTED]")
	return gitAccessToken.ReplaceAllString(s, "${1}[REDACTED]")
}

// runGit runs git and returns stdout. A non-zero exit is an error; stderr is
// included for diagnosis but a successful command may still write progress there
// (notably `git push`), so it is not treated as a failure.
func runGit(ctx context.Context, gitBin, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, gitBin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("publish: git %s: %w (%s)", args[0], err, redactGitStderr(strings.TrimSpace(stderr.String())))
	}
	return stdout.String(), nil
}

// tokenGitEnv passes the installation token through GIT_CONFIG_* as an HTTP
// Authorization header. It never reaches argv, the repository config, or a log
// line (FR-40).
func tokenGitEnv(token string) []string {
	env := []string{"GIT_TERMINAL_PROMPT=0"}
	if token == "" {
		return env
	}
	credential := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	return append(env,
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.extraheader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic "+credential,
	)
}
