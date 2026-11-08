// Package api is the validator's HTTP surface (spec 30, FR-11).
//
// Only the bundle endpoint exists in Unit 1. Every request is authenticated by
// the client certificate that tlsconf already verified; the certificate is then
// resolved to a Sandbox identity by the identity package. A request that cannot
// be resolved is a 401 and never reaches the cache.
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/bundle"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/identity"
)

// Server wires the bundle cache and identity resolver into an HTTP handler.
type Server struct {
	Cache    *bundle.Cache
	Resolver *identity.Resolver
	Log      *slog.Logger
}

// Handler returns the validator's routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /v1/repos/{repo}/bundle", s.handleBundle)
	return mux
}

func (s *Server) handleBundle(w http.ResponseWriter, r *http.Request) {
	repo := r.PathValue("repo")
	id, err := s.authenticate(r)
	if err != nil {
		// The reason names the rule, never the certificate bytes.
		writeError(w, http.StatusUnauthorized, "unauthenticated", err.Error())
		s.logRequest(r, repo, "", "", "reject", "unauthenticated")
		return
	}
	if !bundle.ValidRepo(repo) {
		writeError(w, http.StatusNotFound, "unknown-repo", "repository is not served")
		s.logRequest(r, repo, string(id.SandboxUID), "", "reject", "unknown-repo")
		return
	}

	b, err := s.Cache.Get(r.Context(), repo)
	if err != nil {
		writeError(w, http.StatusBadGateway, "bundle-unavailable", "bundle could not be fetched")
		s.logRequest(r, repo, string(id.SandboxUID), "", "reject", "bundle-unavailable")
		return
	}

	w.Header().Set("Content-Type", "application/x-git-bundle")
	w.Header().Set("X-Bundle-Commit", b.Commit)
	w.Header().Set("Content-Length", itoa(len(b.Content)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b.Content)
	s.logRequest(r, repo, string(id.SandboxUID), b.Commit, "accept", "")
}

// authenticate resolves the verified peer certificate to a Sandbox identity.
func (s *Server) authenticate(r *http.Request) (identity.Identity, error) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return identity.Identity{}, identity.ErrMalformedSAN
	}
	leaf := r.TLS.PeerCertificates[0]
	if len(leaf.URIs) != 1 {
		return identity.Identity{}, identity.ErrMalformedSAN
	}
	return s.Resolver.Resolve(r.Context(), leaf.URIs[0].String())
}

func (s *Server) logRequest(r *http.Request, repo, sandboxUID, commit, decision, rule string) {
	if s.Log == nil {
		return
	}
	s.Log.Info("request",
		"request_id", r.Header.Get("X-Request-Id"),
		"sandbox_uid", sandboxUID,
		"repo", repo,
		"commit", commit,
		"path", r.URL.Path,
		"decision", decision,
		"rule_id", rule,
	)
}

func writeError(w http.ResponseWriter, status int, ruleID, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"rule_id": strings.TrimSpace(ruleID),
		"reason":  reason,
	})
}

func itoa(n int) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%10]
		n /= 10
	}
	return string(buf[i:])
}
