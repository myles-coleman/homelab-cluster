package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/audit"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/canon"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/patch"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/publish"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/refname"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/rules"
)

const (
	maxPatchBodyBytes = 1 << 20 // patch text
	maxTitleBytes     = 256
	maxBodyBytes      = 32 << 10
	maxMessageBytes   = 4 << 10
)

// handlePatches accepts one patch, verifies it, and returns the four hashes
// (FR-16, FR-17, FR-31). Exactly one audit record is written per request; a
// rejection leaves no artifact behind.
func (s *Server) handlePatches(w http.ResponseWriter, r *http.Request) {
	started := s.now()
	rec := audit.Record{
		RequestID: requestID(r),
		Decision:  audit.Reject,
	}
	defer func() {
		rec.DurationMS = s.now().Sub(started).Milliseconds()
		if s.Audit != nil {
			_ = s.Audit.Write(rec)
		}
	}()

	id, err := s.authenticate(r)
	if err != nil {
		rec.RuleID = rules.Unauthenticated
		writeError(w, http.StatusUnauthorized, rules.Unauthenticated, "client certificate is not a valid sandbox identity")
		return
	}
	rec.SandboxUID = string(id.SandboxUID)

	var req patchRequest
	body := http.MaxBytesReader(w, r.Body, 2*maxPatchBodyBytes)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		rec.RuleID = rules.BadRequest
		writeError(w, http.StatusBadRequest, rules.BadRequest, "request body is not a valid patch submission")
		return
	}
	if dec.More() {
		rec.RuleID = rules.BadRequest
		writeError(w, http.StatusBadRequest, rules.BadRequest, "request body has trailing data")
		return
	}

	rec.Repo = req.Repo
	rec.BaseCommit = req.BaseCommit
	if len(req.Title) > maxTitleBytes || len(req.Body) > maxBodyBytes || len(req.CommitMessage) > maxMessageBytes {
		rec.RuleID = rules.BadRequest
		writeError(w, http.StatusBadRequest, rules.BadRequest, "a metadata field is over its size limit")
		return
	}
	if req.Patch == "" {
		rec.RuleID = rules.BadRequest
		writeError(w, http.StatusBadRequest, rules.BadRequest, "patch is required")
		return
	}

	slug := strings.TrimSpace(req.Slug)
	if slug == "" {
		slug = slugify(req.Title)
	}
	ref, err := refname.Build(string(id.SandboxUID), slug)
	if err != nil {
		rec.RuleID = rules.InvalidRef
		writeError(w, http.StatusUnprocessableEntity, rules.InvalidRef, "ref must be agent/<sandbox-uid>/<slug>")
		return
	}
	rec.Ref = ref

	if s.Applier == nil {
		rec.RuleID = "internal-error"
		writeError(w, http.StatusInternalServerError, "internal-error", "verification is not configured")
		return
	}

	res, err := s.Applier.Apply(r.Context(), patch.Request{
		Repo:          req.Repo,
		Ref:           ref,
		BaseCommit:    req.BaseCommit,
		Title:         req.Title,
		Body:          req.Body,
		CommitMessage: req.CommitMessage,
		Patch:         []byte(req.Patch),
		SandboxUID:    string(id.SandboxUID),
	})
	if err != nil {
		if rej, ok := rules.From(err); ok {
			rec.RuleID = rej.RuleID
			writeError(w, statusFor(rej.RuleID), rej.RuleID, rej.Reason)
			return
		}
		// Unexpected failure: log it, tell the caller nothing about internals.
		if s.Log != nil {
			s.Log.Error("patch pipeline failed", "request_id", rec.RequestID, "err", err)
		}
		rec.RuleID = "internal-error"
		writeError(w, http.StatusInternalServerError, "internal-error", "verification failed")
		return
	}

	canonicalHash, err := canon.Hash(req.Repo, ref, res.CommitSHA, res.TreeSHA, req.Title, req.Body)
	if err != nil {
		rec.RuleID = "internal-error"
		writeError(w, http.StatusInternalServerError, "internal-error", "hash computation failed")
		return
	}

	rec.CommitSHA = res.CommitSHA
	rec.TreeSHA = res.TreeSHA
	rec.CanonicalHash = canonicalHash
	rec.BundleSHA256 = canon.BundleSHA256(res.Bundle)

	// FR-33: the publisher is the only writer. It must have accepted the exact
	// bundle before the sandbox sees a 202; a rejection is forwarded unchanged.
	if s.Publisher != nil {
		if err := s.Publisher.Publish(r.Context(), publish.PublishRequest{
			Repo:          req.Repo,
			Bundle:        res.Bundle,
			BundleSHA256:  rec.BundleSHA256,
			CanonicalHash: canonicalHash,
		}); err != nil {
			if rej, ok := rules.From(err); ok {
				rec.RuleID = rej.RuleID
				writeError(w, statusFor(rej.RuleID), rej.RuleID, rej.Reason)
				return
			}
			if s.Log != nil {
				s.Log.Error("publish failed", "request_id", rec.RequestID, "err", err)
			}
			rec.RuleID = rules.PublishUnavailable
			writeError(w, http.StatusBadGateway, rules.PublishUnavailable, "publisher is unavailable")
			return
		}
	}

	rec.Decision = audit.Accept
	rec.RuleID = ""

	writeJSON(w, http.StatusAccepted, patchResponse{
		BundleSHA256:  rec.BundleSHA256,
		CanonicalHash: canonicalHash,
		CommitSHA:     res.CommitSHA,
		TreeSHA:       res.TreeSHA,
	})
	if s.Log != nil {
		s.Log.Info("patch accepted",
			"request_id", rec.RequestID,
			"sandbox_uid", rec.SandboxUID,
			"repo", rec.Repo,
			"commit", rec.CommitSHA,
			"files", len(res.Files),
		)
	}
}

// statusFor maps a stable rule id to the HTTP status the contract promises.
func statusFor(ruleID string) int {
	switch ruleID {
	case rules.BadRequest:
		return http.StatusBadRequest
	case rules.UnknownRepo:
		return http.StatusNotFound
	case rules.StaleBase:
		return http.StatusConflict
	case rules.HashMismatch, rules.BranchExists:
		return http.StatusConflict
	case rules.InvalidRef:
		return http.StatusUnprocessableEntity
	case rules.QuotaExceeded:
		return http.StatusTooManyRequests
	case rules.Unauthenticated:
		return http.StatusUnauthorized
	case rules.PublishUnavailable, rules.PublishFailed:
		return http.StatusBadGateway
	default:
		return http.StatusUnprocessableEntity
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func requestID(r *http.Request) string {
	if id := strings.TrimSpace(r.Header.Get("X-Request-Id")); id != "" {
		return id
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}

// slugify derives a branch slug from the title when the request omits one.
// FR-30 still governs the result; an unusable title simply yields "change".
func slugify(title string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
		if b.Len() >= 41 {
			break
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "change"
	}
	if len(slug) > 41 {
		slug = strings.TrimRight(slug[:41], "-")
	}
	return slug
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

var _ = errors.Is
