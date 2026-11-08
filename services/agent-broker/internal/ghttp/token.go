// Package ghttp mints GitHub App credentials per operation (spec 30, FR-13,
// FR-39, FR-40).
//
// Rules that the implementation must keep:
//   - the JWT is signed with the App private key and lives minutes, not hours;
//   - every installation token is minted for one repository and one permission
//     set, and is never cached across operations;
//   - a token that reports an expiry more than one hour out is treated as a
//     protocol error rather than used.
package ghttp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// defaultAPIBase is the public GitHub API. Production callers normally leave
	// it alone; tests inject an httptest URL through the unexported field.
	defaultAPIBase = "https://api.github.com"
	// jwtLifetime is the signed-JWT window. GitHub accepts at most ten minutes;
	// nine leaves room for clock skew.
	jwtLifetime = 9 * time.Minute
	// jwtBackdate absorbs clock skew between this host and GitHub.
	jwtBackdate = 60 * time.Second
	// maxTokenLifetime is the longest installation-token expiry this broker will
	// trust (FR-13).
	maxTokenLifetime = time.Hour
	// apiTimeout bounds one GitHub round trip.
	apiTimeout = 30 * time.Second
)

// Token is a short-lived installation token.
type Token struct {
	Value     string
	ExpiresAt time.Time
}

// AppClient mints installation tokens for one App installation.
type AppClient struct {
	appID          int64
	installationID int64
	privateKeyPEM  []byte
	httpClient     *http.Client
	apiBase        string
	now            func() time.Time
}

// NewAppClient builds a client. The HTTP client may be nil, in which case a
// default client with a bounded timeout is used.
func NewAppClient(appID, installationID int64, privateKeyPEM []byte) (*AppClient, error) {
	if _, err := parsePrivateKey(privateKeyPEM); err != nil {
		return nil, err
	}
	return &AppClient{
		appID:          appID,
		installationID: installationID,
		privateKeyPEM:  append([]byte(nil), privateKeyPEM...),
		httpClient:     &http.Client{Timeout: apiTimeout},
		apiBase:        defaultAPIBase,
		now:            time.Now,
	}, nil
}

// InstallationToken mints a token narrowed to owner/repo and permissions.
// It performs no caching: callers mint per operation (FR-40).
func (c *AppClient) InstallationToken(ctx context.Context, owner, repo string, permissions map[string]string) (Token, error) {
	jwt, err := c.mintJWT()
	if err != nil {
		return Token{}, err
	}

	payload := struct {
		Repositories []string          `json:"repositories"`
		Permissions  map[string]string `json:"permissions"`
	}{
		Repositories: []string{repo},
		Permissions:  permissions,
	}
	if payload.Permissions == nil {
		payload.Permissions = map[string]string{}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Token{}, fmt.Errorf("ghttp: encode token request: %w", err)
	}

	endpoint := strings.TrimRight(c.apiBase, "/") + "/app/installations/" + strconv.FormatInt(c.installationID, 10) + "/access_tokens"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Token{}, fmt.Errorf("ghttp: build token request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Token{}, fmt.Errorf("ghttp: request installation token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		// Only the status code: the response body is not needed to act and may
		// contain account details unsuitable for logs.
		return Token{}, fmt.Errorf("ghttp: installation token request returned status %d", resp.StatusCode)
	}

	var decoded struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&decoded); err != nil {
		return Token{}, fmt.Errorf("ghttp: decode token response: %w", err)
	}
	if decoded.Token == "" {
		return Token{}, errors.New("ghttp: installation token response is empty")
	}
	expiresAt, err := time.Parse(time.RFC3339, decoded.ExpiresAt)
	if err != nil {
		return Token{}, fmt.Errorf("ghttp: parse token expiry: %w", err)
	}
	now := c.currentTime()
	if !expiresAt.After(now) {
		return Token{}, fmt.Errorf("ghttp: token expiry %s is not in the future", expiresAt.Format(time.RFC3339))
	}
	if expiresAt.After(now.Add(maxTokenLifetime)) {
		return Token{}, fmt.Errorf("ghttp: token expiry %s is more than %s in the future", expiresAt.Format(time.RFC3339), maxTokenLifetime)
	}

	return Token{Value: decoded.Token, ExpiresAt: expiresAt}, nil
}

// mintJWT builds the RS256 App assertion GitHub expects on every request. The
// signature covers the exact base64url header and payload segments.
func (c *AppClient) mintJWT() (string, error) {
	key, err := parsePrivateKey(c.privateKeyPEM)
	if err != nil {
		return "", err
	}

	headerJSON, err := json.Marshal(struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}{Alg: "RS256", Typ: "JWT"})
	if err != nil {
		return "", fmt.Errorf("ghttp: encode JWT header: %w", err)
	}
	now := c.currentTime()
	claimsJSON, err := json.Marshal(struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}{
		Iss: strconv.FormatInt(c.appID, 10),
		Iat: now.Add(-jwtBackdate).Unix(),
		Exp: now.Add(jwtLifetime).Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("ghttp: encode JWT claims: %w", err)
	}

	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("ghttp: sign JWT: %w", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// currentTime tolerates a zero-valued clock injected by a caller rather than
// panicking on every token request.
func (c *AppClient) currentTime() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}

// parsePrivateKey accepts a PKCS#1 RSA key or a PKCS#8 RSA key. Anything else is
// rejected here so a broken key fails at construction, not mid-flight.
func parsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	if len(pemBytes) == 0 {
		return nil, errors.New("ghttp: private key is empty")
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("ghttp: private key is not PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ghttp: parse private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("ghttp: private key is not an RSA key")
	}
	return key, nil
}
