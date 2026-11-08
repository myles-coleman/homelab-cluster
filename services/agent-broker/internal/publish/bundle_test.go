package publish

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type stubTokens struct {
	calls int
	perm  map[string]string
	repo  string
	err   error
}

func (s *stubTokens) InstallationToken(_ context.Context, _ string, repo string, perms map[string]string) (Token, error) {
	s.calls++
	s.repo = repo
	s.perm = perms
	if s.err != nil {
		return Token{}, s.err
	}
	return Token{Value: "ghs_test"}, nil
}

type stubBundle struct {
	commit string
	body   []byte
	token  string
	err    error
}

func (s *stubBundle) Fetch(_ context.Context, _ string, token string) (string, []byte, error) {
	s.token = token
	return s.commit, s.body, s.err
}

func requestWithSAN(t *testing.T, target, san string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, target, nil)
	if san != "" {
		u, err := url.Parse(san)
		if err != nil {
			t.Fatalf("parse %q: %v", san, err)
		}
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{URIs: []*url.URL{u}}}}
	}
	return r
}

func TestBundleRequiresValidatorIdentity(t *testing.T) {
	s := &BundleServer{Owner: "o", Tokens: &stubTokens{}, Fetcher: &stubBundle{}}
	for name, san := range map[string]string{
		"no certificate":    "",
		"foreign identity":  "spiffe://cowlab.org/ns/agent-sandbox/pod/opencode",
		"similar but wrong": "spiffe://cowlab.org/ns/agent-validator/service/other",
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, requestWithSAN(t, "/internal/bundle/nixos", san))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
		})
	}
}

func TestBundleServesRepoWithMintedToken(t *testing.T) {
	tokens := &stubTokens{}
	fetcher := &stubBundle{commit: "deadbeef", body: []byte("bundle")}
	s := &BundleServer{Owner: "myles-coleman", Tokens: tokens, Fetcher: fetcher}

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, requestWithSAN(t, "/internal/bundle/nixos", ValidatorSAN))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if tokens.calls != 1 {
		t.Fatalf("token mints = %d, want exactly 1 per fetch", tokens.calls)
	}
	if tokens.repo != "nixos" || tokens.perm["contents"] != "read" || len(tokens.perm) != 1 {
		t.Errorf("token scope = repo %q perms %v, want nixos and only contents:read", tokens.repo, tokens.perm)
	}
	if fetcher.token != "ghs_test" {
		t.Errorf("fetcher token = %q, want the minted token", fetcher.token)
	}
	if got := rec.Header().Get("X-Bundle-Commit"); got != "deadbeef" {
		t.Errorf("X-Bundle-Commit = %q, want deadbeef", got)
	}
	if rec.Body.String() != "bundle" {
		t.Errorf("body = %q, want bundle", rec.Body.String())
	}
}

func TestBundleRejectsUnknownRepo(t *testing.T) {
	s := &BundleServer{Owner: "o", Tokens: &stubTokens{}, Fetcher: &stubBundle{}}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, requestWithSAN(t, "/internal/bundle/terraform", ValidatorSAN))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "unknown-repo") {
		t.Errorf("body = %s, want unknown-repo", rec.Body.String())
	}
}

func TestBundleReportsTokenFailure(t *testing.T) {
	s := &BundleServer{Owner: "o", Tokens: &stubTokens{err: errors.New("nope")}, Fetcher: &stubBundle{}}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, requestWithSAN(t, "/internal/bundle/nixos", ValidatorSAN))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "nope") {
		t.Errorf("error body leaked the token error: %s", rec.Body.String())
	}
}
