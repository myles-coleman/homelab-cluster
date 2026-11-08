package identity

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/tlsconf"
)

// countingCache records whether the server ever offered a resumable session.
//
// Resumption is disabled at the server, so no NewSessionTicket should arrive and
// Put must never be called. Asserting the cache instead of the second handshake's
// DidResume flag keeps the guard meaningful: a cache that always misses would
// make DidResume false even if tickets were enabled. (Enabled tickets also stall
// net.Pipe, because the server's ticket write has no reader — the original form of
// this test failed by timeout instead of by its assertion.)
type countingCache struct {
	mu   sync.Mutex
	puts int
}

func (c *countingCache) Get(string) (*tls.ClientSessionState, bool) { return nil, false }

func (c *countingCache) Put(string, *tls.ClientSessionState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.puts++
}

func (c *countingCache) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.puts
}

// TestSessionTicketsAreNotResumed is the FR-6 resumption guard: a workload that
// was deleted must not be able to replay a cached TLS session after its
// certificate is gone.
func TestSessionTicketsAreNotResumed(t *testing.T) {
	creds := newSessionCredentials(t)

	serverCfg, err := tlsconf.ServerConfig(creds.serverCertFile, creds.serverKeyFile, creds.caFile)
	if err != nil {
		t.Fatalf("ServerConfig: %v", err)
	}
	clientCfg, err := tlsconf.ClientConfig(creds.clientCertFile, creds.clientKeyFile, creds.caFile, creds.serverName)
	if err != nil {
		t.Fatalf("ClientConfig: %v", err)
	}

	cache := &countingCache{}
	clientCfg.ClientSessionCache = cache
	// The client is allowed to cache; only the server's policy is under test.
	clientCfg.SessionTicketsDisabled = false

	if state := sessionHandshake(t, serverCfg, clientCfg); state.DidResume {
		t.Fatal("first handshake reported a resumed session")
	}

	if got := cache.count(); got != 0 {
		t.Errorf("server offered %d resumable session ticket(s); ServerConfig must disable session tickets", got)
	}
}

type sessionCredentials struct {
	caFile         string
	serverName     string
	serverCertFile string
	serverKeyFile  string
	clientCertFile string
	clientKeyFile  string
}

func newSessionCredentials(t *testing.T) sessionCredentials {
	t.Helper()

	dir := t.TempDir()
	now := time.Now()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}

	serverName := "publisher.test"
	serverCert, serverKey := sessionLeaf(t, caCert, caKey, "publisher", []string{serverName}, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	clientCert, clientKey := sessionLeaf(t, caCert, caKey, "validator", nil, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})

	return sessionCredentials{
		caFile:         sessionPEMFile(t, dir, "ca.pem", caDER, 0o644),
		serverName:     serverName,
		serverCertFile: sessionPEMFile(t, dir, "server.pem", serverCert, 0o644),
		serverKeyFile:  sessionPEMFile(t, dir, "server-key.pem", serverKey, 0o600),
		clientCertFile: sessionPEMFile(t, dir, "client.pem", clientCert, 0o644),
		clientKeyFile:  sessionPEMFile(t, dir, "client-key.pem", clientKey, 0o600),
	}
}

func sessionLeaf(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, commonName string, dnsNames []string, usages []x509.ExtKeyUsage) (certPEM, keyPEM []byte) {
	t.Helper()

	now := time.Now()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           usages,
		DNSNames:              dnsNames,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create leaf certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal leaf key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func sessionPEMFile(t *testing.T, dir, name string, data []byte, mode os.FileMode) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// sessionHandshake runs one handshake over net.Pipe, then drains the client for a
// short window so any post-handshake NewSessionTicket record is actually read and
// offered to the session cache.
func sessionHandshake(t *testing.T, serverCfg, clientCfg *tls.Config) tls.ConnectionState {
	t.Helper()

	serverConn, clientConn := net.Pipe()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverDone := make(chan error, 1)
	type result struct {
		state tls.ConnectionState
		err   error
	}
	clientDone := make(chan result, 1)

	go func() {
		defer serverConn.Close()
		conn := tls.Server(serverConn, serverCfg)
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		if err := conn.HandshakeContext(ctx); err != nil {
			serverDone <- err
			return
		}
		// Drain until the client closes so a ticket write is never left blocked.
		buf := make([]byte, 32)
		for {
			if _, err := conn.Read(buf); err != nil {
				serverDone <- nil
				return
			}
		}
	}()

	go func() {
		defer clientConn.Close()
		conn := tls.Client(clientConn, clientCfg)
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		if err := conn.HandshakeContext(ctx); err != nil {
			clientDone <- result{err: err}
			return
		}
		// Give the server's ticket record a reader, then return regardless.
		_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		buf := make([]byte, 32)
		_, _ = conn.Read(buf)
		clientDone <- result{state: conn.ConnectionState()}
	}()

	out := <-clientDone
	if out.err != nil {
		t.Fatalf("client handshake: %v", out.err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server handshake: %v", err)
	}
	return out.state
}
