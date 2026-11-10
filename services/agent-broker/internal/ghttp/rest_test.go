package ghttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/publish"
)

var _ publish.GitHub = (*REST)(nil)

const (
	restOwner      = "myles-coleman"
	restRepo       = "nixos"
	restTestUID    = "4a22ef3b-312b-4624-8e64-9a3060579091"
	restAgentHead  = "refs/heads/agent/" + restTestUID + "/one"
	restAgentHead2 = "refs/heads/agent/" + restTestUID + "/two"
	restHumanHead  = "refs/heads/feature/human"
)

type mintRecord struct {
	body []byte
}

type restRecord struct {
	method      string
	escapedPath string
	rawQuery    string
	body        []byte
}

type restHarness struct {
	t      *testing.T
	server *httptest.Server
	rest   *REST
	now    time.Time
	token  string
	logBuf *bytes.Buffer

	mu       sync.Mutex
	mints    []mintRecord
	requests []restRecord
}

func newRESTHarness(t *testing.T) *restHarness {
	t.Helper()
	_, keyPEM := newAppKey(t)
	now := time.Now().UTC().Truncate(time.Second)
	h := &restHarness{
		t:      t,
		now:    now,
		token:  "ghs_rest_supersecret_token",
		logBuf: &bytes.Buffer{},
	}
	h.server = httptest.NewTLSServer(http.HandlerFunc(h.handle))
	t.Cleanup(h.server.Close)

	app := newTestAppClient(keyPEM, h.server.URL, h.server.Client(), now)
	h.rest = &REST{
		Owner:   restOwner,
		Tokens:  app,
		BaseURL: h.server.URL,
		HTTP:    h.server.Client(),
		Log:     slog.New(slog.NewTextHandler(h.logBuf, nil)),
	}
	t.Cleanup(func() {
		if strings.Contains(h.logBuf.String(), h.token) {
			t.Errorf("REST client logged the installation token: %s", h.logBuf.String())
		}
	})
	return h
}

func (h *restHarness) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost &&
		strings.HasPrefix(r.URL.Path, "/app/installations/") &&
		strings.HasSuffix(r.URL.Path, "/access_tokens"):
		body, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.mints = append(h.mints, mintRecord{body: body})
		h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token":      h.token,
			"expires_at": h.now.Add(30 * time.Minute).UTC().Format(time.RFC3339),
		})
		return

	case r.Method == http.MethodGet &&
		strings.HasPrefix(r.URL.Path, "/repos/") &&
		strings.HasSuffix(r.URL.Path, "/installation"):
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 99})
		return
	}

	body, _ := io.ReadAll(r.Body)
	h.mu.Lock()
	h.requests = append(h.requests, restRecord{
		method:      r.Method,
		escapedPath: r.URL.EscapedPath(),
		rawQuery:    r.URL.RawQuery,
		body:        body,
	})
	h.mu.Unlock()

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/app":
		writeRESTJSON(w, http.StatusOK, map[string]any{"slug": "agent-broker-publisher"})

	case r.Method == http.MethodGet && r.URL.Path == "/repos/"+restOwner+"/"+restRepo+"/pulls":
		h.listPulls(w, r)

	case r.Method == http.MethodPost && r.URL.Path == "/repos/"+restOwner+"/"+restRepo+"/pulls":
		writeRESTJSON(w, http.StatusCreated, restPR(9, restAgentHead, "agent-broker[bot]", h.now, true))

	case r.Method == http.MethodPatch && r.URL.Path == "/repos/"+restOwner+"/"+restRepo+"/pulls/9":
		writeRESTJSON(w, http.StatusOK, map[string]any{})

	case r.Method == http.MethodDelete &&
		strings.HasPrefix(r.URL.Path, "/repos/"+restOwner+"/"+restRepo+"/git/refs/heads/agent/"):
		w.WriteHeader(http.StatusNoContent)

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (h *restHarness) listPulls(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Query().Get("page") {
	case "", "1":
		w.Header().Set("Link", fmt.Sprintf(`<%s/repos/%s/%s/pulls?state=open&per_page=100&page=2>; rel="next"`, h.server.URL, restOwner, restRepo))
		writeRESTJSON(w, http.StatusOK, []any{
			restPR(1, restHumanHead, "myles-coleman", h.now.Add(-time.Hour), false),
			restPR(2, restAgentHead, "agent-broker[bot]", h.now.Add(-time.Hour), true),
		})
	default:
		writeRESTJSON(w, http.StatusOK, []any{
			restPR(3, restAgentHead2, "agent-broker[bot]", h.now.Add(-2*time.Hour), true),
		})
	}
}

func restPR(number int, head, author string, createdAt time.Time, draft bool) map[string]any {
	// GitHub's pull-request objects report head.ref as the bare branch name
	// (agent/<uid>/<slug>), never as refs/heads/agent/... . Mirroring that on
	// the wire is the point of this fixture: the REST client must normalize the
	// bare head back to a full ref before returning a publish.PullRequest.
	return map[string]any{
		"number":     number,
		"head":       map[string]string{"ref": strings.TrimPrefix(head, "refs/heads/")},
		"user":       map[string]string{"login": author},
		"created_at": createdAt.UTC().Format(time.RFC3339),
		"draft":      draft,
	}
}

func writeRESTJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *restHarness) mintSnapshot() []mintRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]mintRecord(nil), h.mints...)
}

func (h *restHarness) requestSnapshot() []restRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]restRecord(nil), h.requests...)
}

type mintedTokenRequest struct {
	Repositories []string          `json:"repositories"`
	Permissions  map[string]string `json:"permissions"`
}

func assertTokenCount(t *testing.T, h *restHarness, want int) []mintedTokenRequest {
	t.Helper()
	mints := h.mintSnapshot()
	if len(mints) != want {
		t.Fatalf("token mints = %d, want %d (tokens must be minted per operation)", len(mints), want)
	}
	out := make([]mintedTokenRequest, 0, len(mints))
	for _, mint := range mints {
		var decoded mintedTokenRequest
		if err := json.Unmarshal(mint.body, &decoded); err != nil {
			t.Fatalf("decode token request %q: %v", mint.body, err)
		}
		out = append(out, decoded)
	}
	return out
}

func assertSingleToken(t *testing.T, h *restHarness, repo, permission, level string) {
	t.Helper()
	reqs := assertTokenCount(t, h, 1)
	req := reqs[0]
	if len(req.Repositories) != 1 || req.Repositories[0] != repo {
		t.Errorf("token repositories = %v, want exactly [%s]", req.Repositories, repo)
	}
	if len(req.Permissions) != 1 || req.Permissions[permission] != level {
		t.Errorf("token permissions = %v, want exactly {%s: %s}", req.Permissions, permission, level)
	}
}

func findRequest(t *testing.T, h *restHarness, method, escapedPath string) restRecord {
	t.Helper()
	for _, rec := range h.requestSnapshot() {
		if rec.method == method && rec.escapedPath == escapedPath {
			return rec
		}
	}
	t.Fatalf("no %s %s request; got %+v", method, escapedPath, h.requestSnapshot())
	return restRecord{}
}

// TestRESTListOpenAgentPRs covers FR-39's read path: one pull_requests:read
// token narrowed to one repo, pagination followed, and only agent/* PRs
// returned. Counting human PRs as quota would be a denial-of-service vector.
func TestRESTListOpenAgentPRs(t *testing.T) {
	h := newRESTHarness(t)

	got, err := h.rest.ListOpenAgentPRs(context.Background(), restRepo)
	if err != nil {
		t.Fatalf("ListOpenAgentPRs: %v", err)
	}
	if len(got) != 2 || got[0].Number != 2 || got[1].Number != 3 {
		t.Fatalf("PRs = %+v, want numbers [2 3] (human PR filtered out)", got)
	}
	first := got[0]
	if first.Head != restAgentHead {
		t.Errorf("Head = %q, want %q", first.Head, restAgentHead)
	}
	if first.Author != "agent-broker[bot]" {
		t.Errorf("Author = %q, want agent-broker[bot]", first.Author)
	}
	if !first.Draft {
		t.Error("Draft = false, want true")
	}
	if want := h.now.Add(-time.Hour); !first.CreatedAt.Equal(want) {
		t.Errorf("CreatedAt = %v, want %v", first.CreatedAt, want)
	}

	assertSingleToken(t, h, restRepo, "pull_requests", "read")

	rec := findRequest(t, h, http.MethodGet, "/repos/"+restOwner+"/"+restRepo+"/pulls")
	if !strings.Contains(rec.rawQuery, "state=open") {
		t.Errorf("query = %q, want state=open", rec.rawQuery)
	}
	if !strings.Contains(rec.rawQuery, "per_page=100") {
		t.Errorf("query = %q, want per_page=100", rec.rawQuery)
	}
}

// TestRESTFindOpenPRByHead covers FR-39's find operation: the same read
// permission, a nil result when no open PR matches, and one token per call.
func TestRESTFindOpenPRByHead(t *testing.T) {
	h := newRESTHarness(t)

	got, err := h.rest.FindOpenPRByHead(context.Background(), restRepo, restAgentHead)
	if err != nil {
		t.Fatalf("FindOpenPRByHead: %v", err)
	}
	if got == nil || got.Number != 2 {
		t.Fatalf("found = %+v, want PR 2", got)
	}

	missing, err := h.rest.FindOpenPRByHead(context.Background(), restRepo, "refs/heads/agent/"+restTestUID+"/missing")
	if err != nil {
		t.Fatalf("FindOpenPRByHead(missing): %v", err)
	}
	if missing != nil {
		t.Errorf("missing = %+v, want nil", missing)
	}

	reqs := assertTokenCount(t, h, 2)
	for i, req := range reqs {
		if len(req.Repositories) != 1 || req.Repositories[0] != restRepo {
			t.Errorf("call %d repositories = %v, want exactly [%s]", i+1, req.Repositories, restRepo)
		}
		if len(req.Permissions) != 1 || req.Permissions["pull_requests"] != "read" {
			t.Errorf("call %d permissions = %v, want exactly {pull_requests: read}", i+1, req.Permissions)
		}
	}

	findRequest(t, h, http.MethodGet, "/repos/"+restOwner+"/"+restRepo+"/pulls")
}

// TestRESTCreateDraftPR covers FR-39/FR-36: a pull_requests:write token, the
// exact create body, draft:true, base main, and no merge or review fields.
func TestRESTCreateDraftPR(t *testing.T) {
	h := newRESTHarness(t)
	in := publish.DraftPR{
		Title: "change docs",
		Head:  restAgentHead,
		Base:  "main",
		Body:  "why",
	}

	got, err := h.rest.CreateDraftPR(context.Background(), restRepo, in)
	if err != nil {
		t.Fatalf("CreateDraftPR: %v", err)
	}
	if got.Number != 9 || got.Head != restAgentHead || got.Author != "agent-broker[bot]" || !got.Draft {
		t.Errorf("created PR = %+v, want number 9, agent head, app author, draft", got)
	}

	assertSingleToken(t, h, restRepo, "pull_requests", "write")

	rec := findRequest(t, h, http.MethodPost, "/repos/"+restOwner+"/"+restRepo+"/pulls")
	var body map[string]any
	if err := json.Unmarshal(rec.body, &body); err != nil {
		t.Fatalf("decode create body %q: %v", rec.body, err)
	}
	if body["title"] != in.Title {
		t.Errorf("title = %v, want %q", body["title"], in.Title)
	}
	if body["head"] != "agent/"+restTestUID+"/one" {
		t.Errorf("head = %v, want the branch name without refs/heads/", body["head"])
	}
	if body["base"] != "main" {
		t.Errorf("base = %v, want main", body["base"])
	}
	if body["body"] != in.Body {
		t.Errorf("body = %v, want %q", body["body"], in.Body)
	}
	if body["draft"] != true {
		t.Errorf("draft = %v, want true", body["draft"])
	}
	if _, ok := body["merge"]; ok {
		t.Error("create body contains a merge field")
	}
	if _, ok := body["ready_for_review"]; ok {
		t.Error("create body contains a ready_for_review field")
	}
}

// TestRESTClosePR covers FR-39's close operation: pull_requests:write, PATCH on
// the PR number, and exactly {"state":"closed"}.
func TestRESTClosePR(t *testing.T) {
	h := newRESTHarness(t)

	if err := h.rest.ClosePR(context.Background(), restRepo, 9); err != nil {
		t.Fatalf("ClosePR: %v", err)
	}
	assertSingleToken(t, h, restRepo, "pull_requests", "write")

	rec := findRequest(t, h, http.MethodPatch, "/repos/"+restOwner+"/"+restRepo+"/pulls/9")
	var body map[string]string
	if err := json.Unmarshal(rec.body, &body); err != nil {
		t.Fatalf("decode close body %q: %v", rec.body, err)
	}
	if len(body) != 1 || body["state"] != "closed" {
		t.Errorf("close body = %v, want exactly {state: closed}", body)
	}
}

// TestRESTDeleteBranch covers FR-39/FR-40's branch deletion: a contents:write
// token and the ref path under git/refs/heads. Slashes in the ref stay path
// separators; they must not be flattened into %2F.
func TestRESTDeleteBranch(t *testing.T) {
	h := newRESTHarness(t)
	ref := "refs/heads/agent/" + restTestUID + "/one"

	if err := h.rest.DeleteBranch(context.Background(), restRepo, ref); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
	assertSingleToken(t, h, restRepo, "contents", "write")

	escaped := "/repos/" + restOwner + "/" + restRepo + "/git/refs/heads/agent/" + restTestUID + "/one"
	findRequest(t, h, http.MethodDelete, escaped)
}

// TestRESTDeleteBranchEscapesPathSegments pins FR-39's escaping rule. A ref
// segment containing # is not a valid broker ref, but the REST layer's job is
// to escape each segment, not to reshape the path.
func TestRESTDeleteBranchEscapesPathSegments(t *testing.T) {
	h := newRESTHarness(t)
	ref := "refs/heads/agent/" + restTestUID + "/slug#1"

	if err := h.rest.DeleteBranch(context.Background(), restRepo, ref); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
	escaped := "/repos/" + restOwner + "/" + restRepo + "/git/refs/heads/agent/" + restTestUID + "/slug%231"
	findRequest(t, h, http.MethodDelete, escaped)
}

// TestRESTLogin covers FR-39's login operation. GET /user rejects installation
// tokens (403), so login must come from the App slug via GET /app, which names
// the bot <slug>[bot] — and must not mint an installation token at all.
func TestRESTLogin(t *testing.T) {
	h := newRESTHarness(t)

	login, err := h.rest.Login(context.Background())
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if login != "agent-broker-publisher[bot]" {
		t.Errorf("Login = %q, want agent-broker-publisher[bot]", login)
	}

	assertTokenCount(t, h, 0)
	findRequest(t, h, http.MethodGet, "/app")
}
