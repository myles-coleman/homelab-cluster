// Package tlsconf builds the mutual-TLS configurations shared by the validator
// and the publisher (spec 30, FR-6).
//
// Two properties are load-bearing and must not be relaxed:
//
//   - TLS 1.3 is the minimum version.
//   - Session resumption is disabled, so a deleted workload's cached session
//     cannot outlive its certificate.
//
// Certificates are reloaded from disk on every handshake. The kubelet refreshes
// a mounted Secret in place, and the spec requires a rotated certificate to be
// picked up without restarting the process (FR-7).
package tlsconf

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// ServerConfig returns a server configuration that requires and verifies a
// client certificate signed by clientCAFile, serves certFile/keyFile, and
// reloads that pair on every handshake.
func ServerConfig(certFile, keyFile, clientCAFile string) (*tls.Config, error) {
	// Fail at construction when the operator has clearly misconfigured a path;
	// per-handshake reloading still handles later rotations (FR-7).
	if _, err := loadKeyPair(certFile, keyFile); err != nil {
		return nil, err
	}
	clientCAs, err := loadCertPool(clientCAFile)
	if err != nil {
		return nil, err
	}

	return &tls.Config{
		MinVersion:             tls.VersionTLS13,
		SessionTicketsDisabled: true,
		ClientAuth:             tls.RequireAndVerifyClientCert,
		ClientCAs:              clientCAs,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			cert, err := loadKeyPair(certFile, keyFile)
			if err != nil {
				return nil, err
			}
			return &cert, nil
		},
	}, nil
}

// ClientConfig returns a client configuration that trusts rootCAFile, presents
// certFile/keyFile (reloaded per handshake) and verifies serverName.
func ClientConfig(certFile, keyFile, rootCAFile, serverName string) (*tls.Config, error) {
	if _, err := loadKeyPair(certFile, keyFile); err != nil {
		return nil, err
	}
	rootCAs, err := loadCertPool(rootCAFile)
	if err != nil {
		return nil, err
	}

	return &tls.Config{
		MinVersion:             tls.VersionTLS13,
		SessionTicketsDisabled: true,
		RootCAs:                rootCAs,
		ServerName:             serverName,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			cert, err := loadKeyPair(certFile, keyFile)
			if err != nil {
				return nil, err
			}
			return &cert, nil
		},
	}, nil
}

// loadKeyPair reads the certificate and key from disk. Reading on every call is
// deliberate: a rotated Secret must be picked up without a restart (FR-7). A
// missing or malformed file surfaces as a handshake error, never a panic.
func loadKeyPair(certFile, keyFile string) (tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("tlsconf: load certificate pair (cert %q, key %q): %w", certFile, keyFile, err)
	}
	return cert, nil
}

// loadCertPool builds a pool from one CA file. The file may be a PEM bundle or a
// DER certificate: GitHub's chain is distributed as PEM, while the mounted
// Secret fixtures and some operators use DER. An unreadable file or a file with
// no certificates is a configuration error, not an empty trust store.
func loadCertPool(caFile string) (*x509.CertPool, error) {
	data, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("tlsconf: read CA file %q: %w", caFile, err)
	}
	pool := x509.NewCertPool()
	if pool.AppendCertsFromPEM(data) {
		return pool, nil
	}
	certs, err := x509.ParseCertificates(data)
	if err != nil {
		return nil, fmt.Errorf("tlsconf: CA file %q contains no certificates: %w", caFile, err)
	}
	if len(certs) == 0 {
		return nil, fmt.Errorf("tlsconf: CA file %q contains no certificates", caFile)
	}
	for _, cert := range certs {
		pool.AddCert(cert)
	}
	return pool, nil
}
