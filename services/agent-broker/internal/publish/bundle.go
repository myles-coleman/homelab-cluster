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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/audit"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/rules"
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

// HistoryFetcher produces the full-history bundle the validator uses for patch
// application. It is never served to the sandbox.
type HistoryFetcher interface {
	FetchHistory(ctx context.Context, repo, token string) (commit string, content []byte, err error)
}

// BundleServer is the publisher's read-only bundle endpoint plus the publish
// endpoint on the same validator identity.
type BundleServer struct {
	Owner     string
	Tokens    TokenSource
	Fetcher   BundleFetcher
	History   HistoryFetcher
	Publish   *Service
	Audit     *audit.Logger
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
	mux.HandleFunc("GET /internal/history/{repo}", s.handleHistory)
	mux.HandleFunc("POST /v1/publish", s.handlePublish)
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

// handleHistory serves the full-history bundle to the validator only (FR-12,
// FR-19). Same identity check and same per-fetch token as the agent bundle.
func (s *BundleServer) handleHistory(w http.ResponseWriter, r *http.Request) {
	repo := r.PathValue("repo")
	if !s.admit(r) {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "client identity is not the validator")
		return
	}
	if !ValidRepo(repo) {
		writeError(w, http.StatusNotFound, "unknown-repo", "repository is not served")
		return
	}
	if s.History == nil {
		writeError(w, http.StatusNotImplemented, "history-unavailable", "history endpoint is not configured")
		return
	}

	tok, err := s.Tokens.InstallationToken(r.Context(), s.Owner, repo, map[string]string{"contents": "read"})
	if err != nil {
		writeError(w, http.StatusBadGateway, "token-unavailable", "installation token could not be minted")
		return
	}

	commit, content, err := s.History.FetchHistory(r.Context(), repo, tok.Value)
	if err != nil {
		writeError(w, http.StatusBadGateway, "history-unavailable", "history bundle could not be produced")
		return
	}

	w.Header().Set("Content-Type", "application/x-git-bundle")
	w.Header().Set("X-Bundle-Commit", commit)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

// maxPublishBodyBytes bounds the publish request, including JSON/base64 overhead.
const maxPublishBodyBytes = 48 << 20

// publishResponse is the 201 body: exactly the four contract keys.
type publishResponse struct {
	Number    int    `json:"number"`
	Ref       string `json:"ref"`
	URL       string `json:"url"`
	CommitSHA string `json:"commit_sha"`
}

// handlePublish accepts exactly one verified validator publish (FR-33). The
// validator identity is the same one used for the bundle and history routes.
// Exactly one audit record is written per request, including early rejections.
func (s *BundleServer) handlePublish(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	rec := audit.Record{
		RequestID: publishRequestID(r),
		Decision:  audit.Reject,
	}
	defer func() {
		rec.DurationMS = time.Since(started).Milliseconds()
		if s.Audit != nil {
			_ = s.Audit.Write(rec)
		}
	}()

	reject := func(status int, ruleID, reason string) {
		rec.RuleID = ruleID
		rec.Reason = reason
		rec.RuleOutcomes = []string{ruleID}
		writeError(w, status, ruleID, reason)
	}

	if !s.admit(r) {
		reject(http.StatusUnauthorized, rules.Unauthenticated, "client identity is not the validator")
		return
	}
	if s.Publish == nil {
		reject(http.StatusBadGateway, rules.PublishFailed, "publish is not configured")
		return
	}

	body := http.MaxBytesReader(w, r.Body, maxPublishBodyBytes)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	var req PublishRequest
	if err := dec.Decode(&req); err != nil {
		reject(http.StatusBadRequest, rules.BadRequest, "request body is not a valid publish request")
		return
	}
	if dec.More() {
		reject(http.StatusBadRequest, rules.BadRequest, "request body has trailing data")
		return
	}
	rec.Repo = req.Repo

	res, err := s.Publish.Publish(r.Context(), req)
	if err != nil {
		if rej, ok := rules.From(err); ok {
			reject(publishStatus(rej.RuleID), rej.RuleID, rej.Reason)
			return
		}
		if s.Log != nil {
			s.Log.Error("publish failed", "err", err)
		}
		reject(http.StatusBadGateway, rules.PublishFailed, "publish failed")
		return
	}

	rec.Decision = audit.Accept
	rec.RuleID = ""
	rec.Reason = ""
	rec.RuleOutcomes = []string{audit.Accept}
	rec.CommitSHA = res.CommitSHA
	rec.Ref = res.Ref

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(publishResponse{
		Number:    res.Number,
		Ref:       res.Ref,
		URL:       res.URL,
		CommitSHA: res.CommitSHA,
	})
}

// publishRequestID prefers the caller's correlation id and falls back to a
// random one so every audit record is still addressable.
func publishRequestID(r *http.Request) string {
	if id := strings.TrimSpace(r.Header.Get("X-Request-Id")); id != "" {
		return id
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}

// publishStatus maps a publish rule id to the HTTP status FR-33 pins.
func publishStatus(ruleID string) int {
	switch ruleID {
	case rules.BadRequest:
		return http.StatusBadRequest
	case rules.UnknownRepo:
		return http.StatusNotFound
	case rules.InvalidRef:
		return http.StatusUnprocessableEntity
	case rules.HashMismatch, rules.BranchExists:
		return http.StatusConflict
	case rules.QuotaExceeded:
		return http.StatusTooManyRequests
	case rules.Unauthenticated:
		return http.StatusUnauthorized
	default:
		return http.StatusBadGateway
	}
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
