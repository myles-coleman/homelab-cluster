// Package publish: small git process helpers shared by the verifier and pusher.
package publish

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

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
		return "", fmt.Errorf("publish: git %s: %w (%s)", args[0], err, strings.TrimSpace(stderr.String()))
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
