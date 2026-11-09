// Command validator serves the sandbox-facing broker API (spec 30, Unit 1).
//
// It terminates mTLS with the agent-mtls CA, resolves each client certificate to
// a Sandbox identity, and serves the two repositories it is allowed to serve. It
// holds no GitHub credential: bundles come from the publisher over a second mTLS
// connection with the validator's own client certificate.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/api"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/audit"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/bundle"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/identity"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/patch"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/policy"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/publish"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/scan"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/tlsconf"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/policy"
)

func main() {
	var (
		listen             = flag.String("listen", ":8443", "listen address")
		certFile           = flag.String("cert-file", "/certs/tls.crt", "server certificate")
		keyFile            = flag.String("key-file", "/certs/tls.key", "server private key")
		clientCAFile       = flag.String("client-ca-file", "/certs/ca.crt", "CA that signs sandbox client certificates")
		namespace          = flag.String("namespace", "agent-sandbox", "namespace sandboxes are resolved in")
		publisherURL       = flag.String("publisher-url", "https://publisher.agent.internal:8443", "publisher bundle endpoint")
		publisherCAFile    = flag.String("publisher-ca-file", "/publisher-ca/ca.crt", "CA that signs the publisher certificate")
		publisherCertFile  = flag.String("publisher-cert-file", "/publisher-client/tls.crt", "client certificate presented to the publisher")
		publisherKeyFile   = flag.String("publisher-key-file", "/publisher-client/tls.key", "client key presented to the publisher")
		cacheTTL           = flag.Duration("cache-ttl", 5*time.Minute, "bundle cache TTL")
		shutdownTimeout    = flag.Duration("shutdown-timeout", 15*time.Second, "graceful shutdown budget")
		publisherServerNam = flag.String("publisher-server-name", "publisher.agent.internal", "SNI/verification name for the publisher")
		workDir            = flag.String("work-dir", "/tmp", "writable scratch directory for patch application")
		gitBin             = flag.String("git-bin", "git", "git executable")
	)
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(runConfig{
		listen:             *listen,
		certFile:           *certFile,
		keyFile:            *keyFile,
		clientCAFile:       *clientCAFile,
		namespace:          *namespace,
		publisherURL:       *publisherURL,
		publisherCAFile:    *publisherCAFile,
		publisherCertFile:  *publisherCertFile,
		publisherKeyFile:   *publisherKeyFile,
		publisherServerNam: *publisherServerNam,
		cacheTTL:           *cacheTTL,
		shutdownTimeout:    *shutdownTimeout,
		workDir:            *workDir,
		gitBin:             *gitBin,
		logger:             logger,
	}); err != nil {
		logger.Error("validator exited", "err", err)
		os.Exit(1)
	}
}

type runConfig struct {
	listen             string
	certFile           string
	keyFile            string
	clientCAFile       string
	namespace          string
	publisherURL       string
	publisherCAFile    string
	publisherCertFile  string
	publisherKeyFile   string
	publisherServerNam string
	cacheTTL           time.Duration
	shutdownTimeout    time.Duration
	workDir            string
	gitBin             string
	logger             *slog.Logger
}

func run(cfg runConfig) error {
	serverTLS, err := tlsconf.ServerConfig(cfg.certFile, cfg.keyFile, cfg.clientCAFile)
	if err != nil {
		return err
	}
	clientTLS, err := tlsconf.ClientConfig(cfg.publisherCertFile, cfg.publisherKeyFile, cfg.publisherCAFile, cfg.publisherServerNam)
	if err != nil {
		return err
	}

	restCfg, err := rest.InClusterConfig()
	if err != nil {
		return err
	}
	kube, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return err
	}
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return err
	}

	cache := bundle.NewCache(bundle.NewHTTPFetcher(cfg.publisherURL, clientTLS), cfg.cacheTTL)
	pol, err := policy.Embedded()
	if err != nil {
		return err
	}
	apiServer := &api.Server{
		Cache:      cache,
		Resolver:   identity.NewResolver(kube, dyn, cfg.namespace),
		PolicyYAML: policyfs.YAML,
		Applier: &patch.Applier{
			GitBin:  cfg.gitBin,
			WorkDir: cfg.workDir,
			History: bundle.NewHTTPHistoryFetcher(cfg.publisherURL, clientTLS),
			Policy:  pol,
			Scanner: scan.New(),
		},
		Audit:     audit.New(os.Stdout),
		Log:       cfg.logger,
		Publisher: publish.NewClient(cfg.publisherURL, clientTLS),
	}

	httpServer := &http.Server{
		Addr:              cfg.listen,
		Handler:           apiServer.Handler(),
		TLSConfig:         serverTLS,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(cfg.logger.Handler(), slog.LevelWarn),
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		cfg.logger.Info("validator listening", "addr", cfg.listen)
		errCh <- httpServer.ListenAndServeTLS("", "")
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		cfg.logger.Info("validator shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.shutdownTimeout)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}
