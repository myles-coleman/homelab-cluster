// Package publish serves the publisher's internal bundle endpoint (spec 30,
// FR-12, FR-13).
//
// The publisher is the only component that talks to GitHub. It mints a
// read-only installation token per fetch, narrowed to one repository, clones the
// main tip shallowly, and returns the resulting bundle. The only caller admitted
// is the validator's client identity.
package publish

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// ValidatorSAN is the only client identity allowed to call this endpoint
// (spec 30, FR-12, FR-33).
const ValidatorSAN = "spiffe://cowlab.org/ns/agent-validator/service/validator"

// TokenSource mints a narrow installation token.
type TokenSource interface {
	InstallationToken(ctx context.Context, owner, repo string, permissions map[string]string) (Token, error)
}

// Token mirrors ghttp.Token without importing it, so the handler is testable.
type Token struct {
	Value string
}

// BundleFetcher produces a shallow bundle for one repository using a token the
// caller minted for exactly that fetch.
type BundleFetcher interface {
	Fetch(ctx context.Context, repo, token string) (commit string, content []byte, err error)
}

// BundleServer is the publisher's read-only bundle endpoint.
type BundleServer struct {
	Owner     string
	Tokens    TokenSource
	Fetcher   BundleFetcher
	Log       *slog.Logger
	AdmitSANs []string
}

// Handler returns the publisher's routes.
func (s *BundleServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /internal/bundle/{repo}", s.handleBundle)
	return mux
}

func (s *BundleServer) handleBundle(w http.ResponseWriter, r *http.Request) {
	repo := r.PathValue("repo")
	if !s.admit(r) {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "client identity is not the validator")
		return
	}
	if !ValidRepo(repo) {
		writeError(w, http.StatusNotFound, "unknown-repo", "repository is not served")
		return
	}

	// Mint per fetch, never cached (FR-40).
	tok, err := s.Tokens.InstallationToken(r.Context(), s.Owner, repo, map[string]string{"contents": "read"})
	if err != nil {
		writeError(w, http.StatusBadGateway, "token-unavailable", "installation token could not be minted")
		return
	}

	commit, content, err := s.Fetcher.Fetch(r.Context(), repo, tok.Value)
	if err != nil {
		writeError(w, http.StatusBadGateway, "bundle-unavailable", "bundle could not be produced")
		return
	}

	w.Header().Set("Content-Type", "application/x-git-bundle")
	w.Header().Set("X-Bundle-Commit", commit)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

func (s *BundleServer) admit(r *http.Request) bool {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return false
	}
	allowed := s.AdmitSANs
	if len(allowed) == 0 {
		allowed = []string{ValidatorSAN}
	}
	for _, uri := range r.TLS.PeerCertificates[0].URIs {
		for _, want := range allowed {
			if uri.String() == want {
				return true
			}
		}
	}
	return false
}

// ValidRepo keeps the publisher's allowlist identical to the validator's.
func ValidRepo(repo string) bool {
	switch repo {
	case "nixos", "homelab-cluster":
		return true
	}
	return false
}

func writeError(w http.ResponseWriter, status int, ruleID, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"rule_id": strings.TrimSpace(ruleID),
		"reason":  reason,
	})
}
