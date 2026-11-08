package ghttp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestNewAppClientValidatesPrivateKey rejects unusable key material up front and
// still accepts a well-formed App key: a permissive constructor would only fail
// later, inside a token request where the cause is harder to see.
func TestNewAppClientValidatesPrivateKey(t *testing.T) {
	_, validPEM := newAppKey(t)

	notAKey := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not a private key")})

	tests := []struct {
		name    string
		keyPEM  []byte
		wantErr bool
	}{
		{name: "empty", keyPEM: nil, wantErr: true},
		{name: "not PEM", keyPEM: []byte("this is not PEM"), wantErr: true},
		{name: "PEM with wrong block type", keyPEM: notAKey, wantErr: true},
		{name: "valid RSA key", keyPEM: validPEM, wantErr: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, err := NewAppClient(1234, 99, tc.keyPEM)
			if tc.wantErr {
				if err == nil {
					t.Fatal("NewAppClient accepted invalid private key material")
				}
				return
			}
			if err != nil {
				t.Fatalf("NewAppClient rejected a valid key: %v", err)
			}
			if client == nil {
				t.Fatal("NewAppClient returned a nil client for a valid key")
			}
		})
	}
}

// TestInstallationTokenRequest verifies the FR-13 wire contract end to end: an
// RS256 App JWT in the Authorization header, a request body narrowed to one
// repository and one permission set, and the response mapped into a Token.
func TestInstallationTokenRequest(t *testing.T) {
	key, keyPEM := newAppKey(t)
	// Truncated so the fixture's own RFC3339 encoding (whole seconds) and the
	// assertion below describe the same instant; the check stays exact.
	now := time.Now().UTC().Truncate(time.Second)

	var mu sync.Mutex
	var recorded recordedRequest
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		mu.Lock()
		recorded = recordedRequest{
			method: r.Method,
			path:   r.URL.Path,
			auth:   r.Header.Get("Authorization"),
			body:   body,
		}
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token":      "ghs_installation",
			"expires_at": now.Add(30 * time.Minute).UTC().Format(time.RFC3339),
		})
	}))
	defer server.Close()

	client := newTestAppClient(keyPEM, server.URL, server.Client(), now)

	token, err := client.InstallationToken(context.Background(), "myles-coleman", "nixos", map[string]string{"contents": "read"})
	if err != nil {
		t.Fatalf("InstallationToken: %v", err)
	}

	mu.Lock()
	got := recorded
	mu.Unlock()

	if got.method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.method)
	}
	if want := "/app/installations/99/access_tokens"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}

	assertRS256JWT(t, key, got.auth)

	var body struct {
		Repositories []string          `json:"repositories"`
		Permissions  map[string]string `json:"permissions"`
	}
	if err := json.Unmarshal(got.body, &body); err != nil {
		t.Fatalf("decode request body %q: %v", got.body, err)
	}
	if len(body.Repositories) != 1 || body.Repositories[0] != "nixos" {
		t.Errorf("repositories = %v, want exactly [nixos]", body.Repositories)
	}
	if len(body.Permissions) != 1 || body.Permissions["contents"] != "read" {
		t.Errorf("permissions = %v, want exactly {contents: read}", body.Permissions)
	}

	if token.Value != "ghs_installation" {
		t.Errorf("token = %q, want ghs_installation", token.Value)
	}
	if want := now.Add(30 * time.Minute); !token.ExpiresAt.Equal(want) {
		t.Errorf("expires_at = %v, want %v", token.ExpiresAt, want)
	}
}

// TestInstallationTokenExpiryBoundary treats an expiry beyond one hour as a
// protocol error. A token the broker cannot reason about must not be returned to
// the caller, and the failure must be an error rather than a panic.
func TestInstallationTokenExpiryBoundary(t *testing.T) {
	_, keyPEM := newAppKey(t)
	now := time.Now()

	tests := []struct {
		name      string
		expiresAt time.Time
		wantErr   bool
	}{
		{name: "within one hour", expiresAt: now.Add(30 * time.Minute), wantErr: false},
		{name: "beyond one hour", expiresAt: now.Add(2 * time.Hour), wantErr: true},
		{name: "already expired", expiresAt: now.Add(-time.Minute), wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]string{
					"token":      "ghs_installation",
					"expires_at": tc.expiresAt.UTC().Format(time.RFC3339),
				})
			}))
			defer server.Close()

			client := newTestAppClient(keyPEM, server.URL, server.Client(), now)

			token, err := client.InstallationToken(context.Background(), "myles-coleman", "nixos", map[string]string{"contents": "read"})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("InstallationToken accepted an expiry %v in the future", tc.expiresAt.Sub(now))
				}
				if token != (Token{}) {
					t.Errorf("returned a token %+v alongside an error", token)
				}
				return
			}
			if err != nil {
				t.Fatalf("InstallationToken: %v", err)
			}
			if token.Value == "" {
				t.Error("token value is empty")
			}
		})
	}
}

// TestInstallationTokenIsNotCached proves FR-40: each operation mints its own
// token, so two calls are two POSTs.
func TestInstallationTokenIsNotCached(t *testing.T) {
	_, keyPEM := newAppKey(t)
	now := time.Now()

	var mu sync.Mutex
	posts := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		posts++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token":      "ghs_installation",
			"expires_at": now.Add(30 * time.Minute).UTC().Format(time.RFC3339),
		})
	}))
	defer server.Close()

	client := newTestAppClient(keyPEM, server.URL, server.Client(), now)
	for i := 0; i < 2; i++ {
		if _, err := client.InstallationToken(context.Background(), "myles-coleman", "nixos", map[string]string{"contents": "read"}); err != nil {
			t.Fatalf("InstallationToken call %d: %v", i+1, err)
		}
	}

	mu.Lock()
	got := posts
	mu.Unlock()
	if got != 2 {
		t.Errorf("POST count = %d, want 2 (installation tokens must not be cached)", got)
	}
}

func newTestAppClient(keyPEM []byte, apiBase string, httpClient *http.Client, now time.Time) *AppClient {
	return &AppClient{
		appID:          1234,
		installationID: 99,
		privateKeyPEM:  keyPEM,
		httpClient:     httpClient,
		apiBase:        apiBase,
		now:            func() time.Time { return now },
	}
}

func newAppKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return key, pemBytes
}

func assertRS256JWT(t *testing.T, key *rsa.PrivateKey, authorization string) {
	t.Helper()

	const bearer = "Bearer "
	if !strings.HasPrefix(authorization, bearer) {
		t.Fatalf("Authorization = %q, want a Bearer token", authorization)
	}
	raw := strings.TrimPrefix(authorization, bearer)
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT has %d segments, want 3", len(parts))
	}

	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode JWT header: %v", err)
	}
	var header struct {
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		t.Fatalf("unmarshal JWT header: %v", err)
	}
	if header.Alg != "RS256" {
		t.Errorf("JWT alg = %q, want RS256", header.Alg)
	}

	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode JWT payload: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(payloadJSON, &payload); err != nil {
		t.Fatalf("unmarshal JWT payload: %v", err)
	}
	switch iss := payload["iss"].(type) {
	case float64:
		if int64(iss) != 1234 {
			t.Errorf("JWT iss = %v, want 1234", iss)
		}
	case string:
		if iss != "1234" {
			t.Errorf("JWT iss = %q, want %q", iss, "1234")
		}
	default:
		t.Errorf("JWT iss has type %T, want a number or string", payload["iss"])
	}

	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode JWT signature: %v", err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Errorf("JWT signature does not verify with the App public key: %v", err)
	}
}

type recordedRequest struct {
	method string
	path   string
	auth   string
	body   []byte
}

// TestTokenForRepoResolvesInstallation pins the publisher's credential path: the
// App JWT looks up which installation covers the repository, and the minted token
// is still narrowed to that single repository (FR-13).
func TestTokenForRepoResolvesInstallation(t *testing.T) {
	key, keyPEM := newAppKey(t)
	now := time.Now().UTC().Truncate(time.Second)

	var mu sync.Mutex
	var calls []string
	var mintedBody []byte
	var lookupAuth, mintAuth string

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/myles-coleman/nixos/installation":
			lookupAuth = r.Header.Get("Authorization")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 4242})
		case "/app/installations/4242/access_tokens":
			mintAuth = r.Header.Get("Authorization")
			mintedBody, _ = io.ReadAll(r.Body)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"token":      "ghs_resolved",
				"expires_at": now.Add(30 * time.Minute).UTC().Format(time.RFC3339),
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := newTestAppClient(keyPEM, server.URL, server.Client(), now)
	tok, err := client.TokenForRepo(context.Background(), "myles-coleman", "nixos", map[string]string{"contents": "read"})
	if err != nil {
		t.Fatalf("TokenForRepo: %v", err)
	}
	if tok.Value != "ghs_resolved" {
		t.Errorf("token = %q, want ghs_resolved", tok.Value)
	}

	mu.Lock()
	got := append([]string(nil), calls...)
	mu.Unlock()
	want := []string{"GET /repos/myles-coleman/nixos/installation", "POST /app/installations/4242/access_tokens"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	assertRS256JWT(t, key, lookupAuth)
	assertRS256JWT(t, key, mintAuth)

	var body struct {
		Repositories []string          `json:"repositories"`
		Permissions  map[string]string `json:"permissions"`
	}
	if err := json.Unmarshal(mintedBody, &body); err != nil {
		t.Fatalf("decode minted body: %v", err)
	}
	if len(body.Repositories) != 1 || body.Repositories[0] != "nixos" {
		t.Errorf("repositories = %v, want exactly [nixos]", body.Repositories)
	}
}
