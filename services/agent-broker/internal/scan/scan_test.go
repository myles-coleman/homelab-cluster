package scan

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
)

// The scanner must never echo the value it matched (FR-25): a finding is
// rendered into an HTTP error body and into logs, so it is a leak if the secret
// appears in either field.
func findingText(f Finding) string { return f.Location + "\x00" + f.Reason }

const pemBlock = `-----BEGIN RSA PRIVATE KEY-----
MIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEX6Ppy1tPf9Cnzj4p4WGeKLs1Pt8Qu
KUpRKfFLfRYC9AIKjbJTWit+CqvjWYzvQwECAwEAAQJAIJLixBy2qpFoS4DSmoEm
o3qGy0t6z09AIJtH+5OeRV1be+N4cDYJKffGzDa88vQENZiRm0GRq6a+HPGQMd2k
-----END RSA PRIVATE KEY-----
`

const (
	githubToken = "ghp_0123456789abcdefghijklmnopqrstuvwx"
	awsKey      = "AKIAIOSFODNN7EXAMPLE"
	sshKey      = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK7QZx0m0m2Fq3V0pQx8k1sJ2n4b5c6d7e8f9g0h1i2j agent@host"
	jwt         = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4ifQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"
	password    = `password = "hunter2hunter2"`
)

// TestDetectsPlainShapes covers FR-25's plaintext detector set. Every credential
// shape that appears in the wild must be caught before the bundle is published.
func TestDetectsPlainShapes(t *testing.T) {
	s := New()

	tests := []struct {
		name     string
		input    string
		secretIn string
	}{
		{"pem private key", pemBlock, "BEGIN RSA PRIVATE KEY"},
		{"github token", "token: " + githubToken + "\n", githubToken},
		{"aws access key", "aws_access_key_id = " + awsKey + "\n", awsKey},
		{"ssh public key", "host setup\n" + sshKey + "\n", "AAAAC3NzaC1lZDI1NTE5"},
		{"jwt", "Authorization: Bearer " + jwt + "\n", jwt},
		{"password assignment", "db:\n  " + password + "\n", "hunter2hunter2"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			findings := s.Text("hosts/bee/secrets.nix", []byte(tc.input))
			if len(findings) == 0 {
				t.Fatalf("no findings for %s", tc.name)
			}
			if findings[0].Location != "hosts/bee/secrets.nix" {
				t.Errorf("Location = %q, want %q", findings[0].Location, "hosts/bee/secrets.nix")
			}
			for _, f := range findings {
				if f.Reason == "" {
					t.Errorf("finding has an empty Reason")
				}
				if strings.Contains(findingText(f), tc.secretIn) {
					t.Errorf("finding leaks the matched value: %#v", f)
				}
				if strings.Contains(f.Reason, "\n") {
					t.Errorf("Reason is multi-line: %q", f.Reason)
				}
			}
		})
	}
}

// TestDetectsEncodedShapes covers FR-26: a wrapped secret is still a secret. The
// scanner is expected to unwrap the common encodings before matching.
func TestDetectsEncodedShapes(t *testing.T) {
	s := New()

	tests := []struct {
		name  string
		input string
	}{
		{"base64 pem block", base64.StdEncoding.EncodeToString([]byte(pemBlock))},
		{"hex password", hex.EncodeToString([]byte("password=hunter2hunter2"))},
		{"percent-encoded password", "password%3Dhunter2hunter2"},
		// \u0074\u006f\u006b\u0065\u006e\u003d decodes to "token=".
		{"unicode-escaped token assignment", `\u0074\u006f\u006b\u0065\u006e\u003dhunter2hunter2`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			findings := s.Text("docs/notes.md", []byte(tc.input))
			if len(findings) == 0 {
				t.Fatalf("no findings for encoded payload %q", tc.input)
			}
			for _, f := range findings {
				if f.Location != "docs/notes.md" {
					t.Errorf("Location = %q, want %q", f.Location, "docs/notes.md")
				}
				if f.Reason == "" {
					t.Errorf("finding has an empty Reason")
				}
				if strings.Contains(findingText(f), "hunter2hunter2") {
					t.Errorf("finding leaks the decoded secret: %#v", f)
				}
			}
		})
	}
}

// TestLocationForMetadata covers FR-25's requirement that blame is attributable:
// a secret in the title, body or commit message must be reported against that
// field, not against a repository path.
func TestLocationForMetadata(t *testing.T) {
	s := New()
	locations := []string{"title", "body", "commit_message"}

	for _, location := range locations {
		t.Run(location, func(t *testing.T) {
			findings := s.Text(location, []byte("add support for "+githubToken))
			if len(findings) == 0 {
				t.Fatalf("no findings for a secret in %s", location)
			}
			for _, f := range findings {
				if f.Location != location {
					t.Errorf("Location = %q, want %q", f.Location, location)
				}
			}
		})
	}
}

// TestNoFalsePositives guards the other direction: a noisy scanner blocks
// legitimate changes. A UUID in particular appears in every ref this service
// issues (FR-30), and "token bucket" is ordinary prose.
func TestNoFalsePositives(t *testing.T) {
	s := New()

	tests := []struct{ name, location, input string }{
		{
			name:     "clean go source",
			location: "internal/scan/scan.go",
			input: `package scan

import "strings"

func describe(s string) string {
	return strings.TrimSpace(s)
}
`,
		},
		{
			name:     "readme mentions token bucket",
			location: "docs/x.md",
			input:    "The limiter uses a token bucket: tokens refill at a fixed rate and requests block when the bucket is empty.",
		},
		{
			name:     "uuid",
			location: "docs/x.md",
			input:    "6fa8e2f0-1c3d-4a5b-8c7d-9e0f1a2b3c4d",
		},
		{
			name:     "empty",
			location: "docs/x.md",
			input:    "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if findings := s.Text(tc.location, []byte(tc.input)); len(findings) != 0 {
				t.Errorf("unexpected findings for clean input: %#v", findings)
			}
		})
	}
}

// TestConcurrentUse covers the documented "stateless and safe for concurrent
// use" contract. Run with -race to make a shared buffer or lazy init fail.
func TestConcurrentUse(t *testing.T) {
	s := New()
	input := []byte("token: " + githubToken + "\n")

	const goroutines = 20
	var wg sync.WaitGroup
	results := make([][]Finding, goroutines)

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			results[i] = s.Text("docs/x.md", input)
		}(i)
	}
	wg.Wait()

	for i, findings := range results {
		if len(findings) == 0 {
			t.Errorf("goroutine %d saw no findings", i)
		}
		for _, f := range findings {
			if f.Location != "docs/x.md" {
				t.Errorf("goroutine %d: Location = %q, want %q", i, f.Location, "docs/x.md")
			}
		}
	}
}
