package tlsconf

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
	"testing"
	"time"
)

// TestServerConfigRequiresTLS13MutualAuthAndNoResumption pins the security
// properties FR-6 depends on. Any future edit that relaxes one of these would
// silently reopen the session-replay and downgrade risks the spec calls out.
func TestServerConfigRequiresTLS13MutualAuthAndNoResumption(t *testing.T) {
	creds := newTestCredentials(t)

	cfg, err := ServerConfig(creds.serverCertFile, creds.serverKeyFile, creds.caFile)
	if err != nil {
		t.Fatalf("ServerConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("ServerConfig returned a nil configuration")
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %#x, want tls.VersionTLS13", cfg.MinVersion)
	}
	if !cfg.SessionTicketsDisabled {
		t.Error("SessionTicketsDisabled = false, want true so a deleted workload's session cannot outlive its certificate")
	}
	if cfg.ClientAuth != tls.VerifyClientCertIfGiven {
		t.Errorf("ClientAuth = %v, want tls.VerifyClientCertIfGiven (absent certificate is an HTTP 401, not a handshake failure)", cfg.ClientAuth)
	}
	if cfg.ClientCAs == nil {
		t.Fatal("ClientCAs is nil; a client certificate could not be verified")
	}
	if !certPoolContains(cfg.ClientCAs, creds.caCert.RawSubject) {
		t.Error("ClientCAs does not contain the supplied client CA")
	}
}

// TestClientConfigPinsTLS13AndVerifier covers the client half of FR-6. The
// broker is the client here; a downgrade or a resumed ticket would let it trust
// a stale endpoint identity.
func TestClientConfigPinsTLS13AndVerifier(t *testing.T) {
	creds := newTestCredentials(t)

	cfg, err := ClientConfig(creds.clientCertFile, creds.clientKeyFile, creds.caFile, creds.serverName)
	if err != nil {
		t.Fatalf("ClientConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("ClientConfig returned a nil configuration")
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %#x, want tls.VersionTLS13", cfg.MinVersion)
	}
	if !cfg.SessionTicketsDisabled {
		t.Error("SessionTicketsDisabled = false, want true")
	}
	if cfg.RootCAs == nil {
		t.Fatal("RootCAs is nil; the publisher certificate could not be verified")
	}
	if !certPoolContains(cfg.RootCAs, creds.caCert.RawSubject) {
		t.Error("RootCAs does not contain the supplied root CA")
	}
	if cfg.ServerName != creds.serverName {
		t.Errorf("ServerName = %q, want %q", cfg.ServerName, creds.serverName)
	}
}

// TestMutualTLSHandshake proves the two configurations actually agree on a
// handshake, not merely that their fields look right. Certificates are minted in
// the test so no key material needs to live in the repository.
func TestMutualTLSHandshake(t *testing.T) {
	creds := newTestCredentials(t)

	serverCfg, err := ServerConfig(creds.serverCertFile, creds.serverKeyFile, creds.caFile)
	if err != nil {
		t.Fatalf("ServerConfig: %v", err)
	}
	clientCfg, err := ClientConfig(creds.clientCertFile, creds.clientKeyFile, creds.caFile, creds.serverName)
	if err != nil {
		t.Fatalf("ClientConfig: %v", err)
	}

	state := handshake(t, serverCfg, clientCfg, false)
	if !state.HandshakeComplete {
		t.Fatal("client reports an incomplete handshake")
	}
	if len(state.PeerCertificates) == 0 {
		t.Fatal("client received no server certificate")
	}
	if state.PeerCertificates[0].Subject.CommonName != "publisher" {
		t.Errorf("server certificate CN = %q, want publisher", state.PeerCertificates[0].Subject.CommonName)
	}
}

type testCredentials struct {
	caCert         *x509.Certificate
	caFile         string
	serverName     string
	serverCertFile string
	serverKeyFile  string
	clientCertFile string
	clientKeyFile  string
}

func newTestCredentials(t *testing.T) testCredentials {
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
	serverCertPEM, serverKeyPEM := issueLeaf(t, caCert, caKey, "publisher", []string{serverName}, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	clientCertPEM, clientKeyPEM := issueLeaf(t, caCert, caKey, "validator", nil, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})

	return testCredentials{
		caCert:         caCert,
		caFile:         writePEMFile(t, dir, "ca.pem", caDER, 0o644),
		serverName:     serverName,
		serverCertFile: writePEMFile(t, dir, "server.pem", serverCertPEM, 0o644),
		serverKeyFile:  writePEMFile(t, dir, "server-key.pem", serverKeyPEM, 0o600),
		clientCertFile: writePEMFile(t, dir, "client.pem", clientCertPEM, 0o644),
		clientKeyFile:  writePEMFile(t, dir, "client-key.pem", clientKeyPEM, 0o600),
	}
}

func issueLeaf(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, commonName string, dnsNames []string, usages []x509.ExtKeyUsage) (certPEM, keyPEM []byte) {
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

func writePEMFile(t *testing.T, dir, name string, data []byte, mode os.FileMode) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func certPoolContains(pool *x509.CertPool, rawSubject []byte) bool {
	for _, subject := range pool.Subjects() {
		if string(subject) == string(rawSubject) {
			return true
		}
	}
	return false
}

// handshake runs a TLS handshake over net.Pipe and returns the client's view of
// it. When exchangeData is set the two sides also swap one byte so TLS 1.3
// session tickets (sent after Finished) are consumed by the client.
func handshake(t *testing.T, serverCfg, clientCfg *tls.Config, exchangeData bool) tls.ConnectionState {
	t.Helper()

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverDone := make(chan error, 1)
	type clientResult struct {
		state tls.ConnectionState
		err   error
	}
	clientDone := make(chan clientResult, 1)

	go func() {
		conn := tls.Server(serverConn, serverCfg)
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		if err := conn.HandshakeContext(ctx); err != nil {
			serverDone <- err
			return
		}
		if exchangeData {
			buf := make([]byte, 1)
			if _, err := conn.Read(buf); err != nil {
				serverDone <- err
				return
			}
			if _, err := conn.Write([]byte("y")); err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()

	go func() {
		conn := tls.Client(clientConn, clientCfg)
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		if err := conn.HandshakeContext(ctx); err != nil {
			clientDone <- clientResult{err: err}
			return
		}
		if exchangeData {
			if _, err := conn.Write([]byte("x")); err != nil {
				clientDone <- clientResult{err: err}
				return
			}
			buf := make([]byte, 1)
			if _, err := conn.Read(buf); err != nil {
				clientDone <- clientResult{err: err}
				return
			}
		}
		clientDone <- clientResult{state: conn.ConnectionState()}
	}()

	if err := <-serverDone; err != nil {
		t.Fatalf("server handshake: %v", err)
	}
	result := <-clientDone
	if result.err != nil {
		t.Fatalf("client handshake: %v", result.err)
	}
	return result.state
}
