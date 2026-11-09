// Command publisher serves the internal bundle endpoint and is the only broker
// component that talks to GitHub (spec 30, FR-12, FR-13).
//
// Unit 1 needs only the read path: mint a read-only installation token for one
// repository, clone the main tip shallowly, and return a bundle. The publish
// endpoint arrives in Unit 3 on the same identity.
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

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/ghttp"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/gitbundle"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/publish"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/tlsconf"
)

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
		shutdownTimeout = flag.Duration("shutdown-timeout", 15*time.Second, "graceful shutdown budget")
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
	serverTLS, err := tlsconf.ServerConfig(*certFile, *keyFile, *clientCAFile)
	if err != nil {
		logger.Error("build server TLS", "err", err)
		os.Exit(1)
	}

	gitFetcher := publish.GitFetcher{Owner: *owner, Opts: gitbundle.Options{GitBin: *gitBin, WorkDir: *workDir}}
	handler := (&publish.BundleServer{
		Owner:   *owner,
		Tokens:  appTokens{client: app},
		Fetcher: gitFetcher,
		History: gitFetcher,
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
