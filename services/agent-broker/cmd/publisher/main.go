// Command publisher serves the internal bundle/history endpoints and the
// validator's publish endpoint. It is the only broker component that talks to
// GitHub (spec 30, FR-12, FR-13, FR-33..FR-40).
//
// The read path mints a read-only installation token per fetch. The publish path
// verifies the validator's thin bundle locally, applies quotas, pushes one agent
// branch with one contents:write token, and opens a draft pull request.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/audit"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/ghttp"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/gitbundle"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/publish"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/quotas"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/tlsconf"
)

// servedRepos is the same allowlist the validator enforces (FR-11).
var servedRepos = []string{"nixos", "homelab-cluster"}

func main() {
	var (
		listen          = flag.String("listen", ":8443", "listen address")
		certFile        = flag.String("cert-file", "/certs/tls.crt", "server certificate")
		keyFile         = flag.String("key-file", "/certs/tls.key", "server private key")
		clientCAFile    = flag.String("client-ca-file", "/certs/ca.crt", "CA that signs the validator client certificate")
		owner           = flag.String("owner", "myles-coleman", "repository owner")
		appIDFile       = flag.String("app-id-file", "/app-secret/app-id", "file containing the GitHub App id")
		appKeyFile      = flag.String("app-key-file", "/app-secret/private-key.pem", "GitHub App private key")
		workDir         = flag.String("work-dir", "/tmp", "writable scratch directory")
		gitBin          = flag.String("git-bin", "git", "git executable")
		cleanup         = flag.Bool("cleanup", false, "run one cleanup pass for both repositories and exit")
		cleanupForce    = flag.Bool("cleanup-force", false, "cleanup fresh app-authored branches too")
		maxBranchAge    = flag.Duration("max-branch-age", 72*time.Hour, "maximum age of an agent branch before cleanup")
		shutdownTimeout = flag.Duration("shutdown-timeout", 15*time.Second, "graceful shutdown budget")
		auditLogFile    = flag.String("audit-log-file", "", "local NDJSON audit store; empty disables the file (stdout still ships via filelog)")
		auditRetention  = flag.Duration("audit-retention", 72*time.Hour, "how long the local audit store keeps a record")
	)
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	appIDRaw, err := os.ReadFile(*appIDFile)
	if err != nil {
		logger.Error("read app id", "err", err)
		os.Exit(1)
	}
	appID, err := strconv.ParseInt(strings.TrimSpace(string(appIDRaw)), 10, 64)
	if err != nil || appID <= 0 {
		logger.Error("app id is not a positive integer", "err", err)
		os.Exit(1)
	}
	keyPEM, err := os.ReadFile(*appKeyFile)
	if err != nil {
		logger.Error("read app private key", "err", err)
		os.Exit(1)
	}

	app, err := ghttp.NewAppClient(appID, 0, keyPEM)
	if err != nil {
		logger.Error("build github app client", "err", err)
		os.Exit(1)
	}

	rest := &ghttp.REST{
		Owner:  *owner,
		Tokens: app,
		Log:    logger,
	}

	if *cleanup {
		runCleanup(logger, rest, *maxBranchAge, *cleanupForce)
		return
	}

	// The publisher is deliberately isolated from in-cluster OTLP egress: it
	// writes stdout NDJSON, which the existing filelog collector ships, and
	// optionally mirrors to a local file.
	auditLogger := audit.New(os.Stdout)
	if *auditLogFile != "" {
		auditStore, err := audit.OpenStore(*auditLogFile, *auditRetention)
		if err != nil {
			logger.Error("open audit store", "err", err)
			os.Exit(1)
		}
		defer auditStore.Close()
		auditLogger.Store = auditStore
	}

	serverTLS, err := tlsconf.ServerConfig(*certFile, *keyFile, *clientCAFile)
	if err != nil {
		logger.Error("build server TLS", "err", err)
		os.Exit(1)
	}

	gitOpts := gitbundle.Options{GitBin: *gitBin, WorkDir: *workDir}
	gitFetcher := publish.GitFetcher{Owner: *owner, Opts: gitOpts}
	tokens := appTokens{client: app}

	limiter := quotas.New(quotas.Limits{})
	limiter.OpenPRs = func(ctx context.Context, repo string) (int, error) {
		prs, err := rest.ListOpenAgentPRs(ctx, repo)
		if err != nil {
			return 0, err
		}
		return len(prs), nil
	}

	service := &publish.Service{
		Owner: *owner,
		Verifier: &publish.GitVerifier{
			GitBin:  *gitBin,
			WorkDir: *workDir,
			History: historySource{owner: *owner, opts: gitOpts, tokens: tokens},
		},
		Quotas: limiter,
		GitHub: rest,
		Pusher: &publish.GitPusher{
			Owner:  *owner,
			Opts:   gitOpts,
			Tokens: tokens,
		},
		Log: logger,
	}

	handler := (&publish.BundleServer{
		Owner:   *owner,
		Tokens:  tokens,
		Fetcher: gitFetcher,
		History: gitFetcher,
		Publish: service,
		Audit:   auditLogger,
		Log:     logger,
	}).Handler()

	httpServer := &http.Server{
		Addr:              *listen,
		Handler:           handler,
		TLSConfig:         serverTLS,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("publisher listening", "addr", *listen)
		errCh <- httpServer.ListenAndServeTLS("", "")
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return
		}
		logger.Error("publisher exited", "err", err)
		os.Exit(1)
	case <-ctx.Done():
		logger.Info("publisher shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), *shutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown", "err", err)
		os.Exit(1)
	}
}

// runCleanup is the one-shot operator command (FR-38). It never runs as part of
// the server path.
func runCleanup(logger *slog.Logger, gh publish.GitHub, maxAge time.Duration, force bool) {
	cleaner := &publish.Cleanup{GitHub: gh, MaxAge: maxAge, Force: force, Now: time.Now}
	ctx := context.Background()
	for _, repo := range servedRepos {
		res, err := cleaner.Run(ctx, repo)
		if err != nil {
			logger.Error("cleanup failed", "repo", repo, "err", err)
			os.Exit(1)
		}
		logger.Info("cleanup complete",
			"repo", repo,
			"closed_prs", len(res.ClosedPRs),
			"deleted_refs", len(res.DeletedRefs),
		)
	}
}

// historySource mints a contents:read token per fetch and materialises history
// for the verifier. The token is never cached.
type historySource struct {
	owner  string
	opts   gitbundle.Options
	tokens publish.TokenSource
}

func (h historySource) FetchHistory(ctx context.Context, repo string) (string, []byte, error) {
	tok, err := h.tokens.InstallationToken(ctx, h.owner, repo, map[string]string{"contents": "read"})
	if err != nil {
		return "", nil, err
	}
	opts := h.opts
	opts.Token = tok.Value
	return gitbundle.FetchHistory(ctx, opts, h.owner, repo)
}

// appTokens adapts the App client to the publisher's TokenSource. It resolves the
// installation per call, so no installation id is configured or stored.
type appTokens struct {
	client *ghttp.AppClient
}

func (a appTokens) InstallationToken(ctx context.Context, owner, repo string, permissions map[string]string) (publish.Token, error) {
	tok, err := a.client.TokenForRepo(ctx, owner, repo, permissions)
	if err != nil {
		return publish.Token{}, err
	}
	return publish.Token{Value: tok.Value}, nil
}
