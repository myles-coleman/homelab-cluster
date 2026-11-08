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
	"testing"
	"time"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/tlsconf"
)

// TestSessionTicketsAreNotResumed is the FR-6 resumption guard. ServerConfig
// disables tickets so a workload that was deleted cannot replay a cached TLS
// session after its certificate is gone.
//
// The client's cache is deliberately enabled here: if the server forgot to
// disable tickets the second handshake would resume and this test would fail.
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

	clientCfg.ClientSessionCache = tls.NewLRUClientSessionCache(4)
	// ClientConfig disables resumption too; allow caching so the assertion
	// isolates the server's ticket policy rather than passing trivially.
	clientCfg.SessionTicketsDisabled = false

	first := sessionHandshake(t, serverCfg, clientCfg, true)
	if first.DidResume {
		t.Fatal("first handshake reported a resumed session")
	}

	second := sessionHandshake(t, serverCfg, clientCfg, false)
	if second.DidResume {
		t.Error("second handshake resumed a session; ServerConfig must disable session tickets")
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

// sessionHandshake runs one handshake over net.Pipe and returns the client's
// connection state. When exchangeData is set the peers swap a byte so a TLS 1.3
// NewSessionTicket offered after the handshake is actually consumed.
func sessionHandshake(t *testing.T, serverCfg, clientCfg *tls.Config, exchangeData bool) tls.ConnectionState {
	t.Helper()

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverDone := make(chan error, 1)
	type result struct {
		state tls.ConnectionState
		err   error
	}
	clientDone := make(chan result, 1)

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
			clientDone <- result{err: err}
			return
		}
		if exchangeData {
			if _, err := conn.Write([]byte("x")); err != nil {
				clientDone <- result{err: err}
				return
			}
			buf := make([]byte, 1)
			if _, err := conn.Read(buf); err != nil {
				clientDone <- result{err: err}
				return
			}
		}
		clientDone <- result{state: conn.ConnectionState()}
	}()

	if err := <-serverDone; err != nil {
		t.Fatalf("server handshake: %v", err)
	}
	out := <-clientDone
	if out.err != nil {
		t.Fatalf("client handshake: %v", out.err)
	}
	return out.state
}
