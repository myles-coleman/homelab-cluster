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
	"time"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/audit"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/bundle"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/identity"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/patch"
)

// Server wires the bundle cache, identity resolver, policy and patch pipeline
// into an HTTP handler.
type Server struct {
	Cache    *bundle.Cache
	Resolver *identity.Resolver
	// PolicyYAML is the canonical allowlist served at GET /v1/policy (FR-32).
	PolicyYAML []byte
	Applier    *patch.Applier
	Audit      *audit.Logger
	Log        *slog.Logger
	// Now is injectable for tests; nil means time.Now.
	Now func() time.Time
}

// Handler returns the validator's routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /v1/repos/{repo}/bundle", s.handleBundle)
	mux.HandleFunc("GET /v1/policy", s.handlePolicy)
	mux.HandleFunc("POST /v1/patches", s.handlePatches)
	return mux
}

func (s *Server) handleBundle(w http.ResponseWriter, r *http.Request) {
	repo := r.PathValue("repo")
	id, err := s.authenticate(r)
	if err != nil {
		// Fixed classifier: the resolver's error may name Kubernetes objects and
		// belongs in the operator log, not in a response to an unauthenticated
		// client.
		writeError(w, http.StatusUnauthorized, "unauthenticated", "client certificate is not a valid sandbox identity")
		if s.Log != nil {
			s.Log.Warn("identity rejected", "path", r.URL.Path, "err", err)
		}
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

// handlePolicy serves the same allowlist the validator enforces, so the manual
// collector cannot be wider than the automated path (FR-32).
func (s *Server) handlePolicy(w http.ResponseWriter, r *http.Request) {
	if _, err := s.authenticate(r); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "client certificate is not a valid sandbox identity")
		return
	}
	if len(s.PolicyYAML) == 0 {
		writeError(w, http.StatusServiceUnavailable, "policy-unavailable", "policy is not configured")
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(s.PolicyYAML)
}

// patchRequest is the POST /v1/patches body (FR-16).
type patchRequest struct {
	Repo          string `json:"repo"`
	BaseCommit    string `json:"base_commit"`
	CommitMessage string `json:"commit_message"`
	Title         string `json:"title"`
	Body          string `json:"body"`
	Patch         string `json:"patch"`
	// Slug optionally names the branch component. When empty it is derived from
	// the title, because FR-16's body has no ref and FR-30 still requires a
	// validated agent/<uid>/<slug> shape.
	Slug string `json:"slug,omitempty"`
}

// patchResponse is the 202 body. It has exactly these four keys (FR-16).
type patchResponse struct {
	BundleSHA256  string `json:"bundle_sha256"`
	CanonicalHash string `json:"canonical_hash"`
	CommitSHA     string `json:"commit_sha"`
	TreeSHA       string `json:"tree_sha"`
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
