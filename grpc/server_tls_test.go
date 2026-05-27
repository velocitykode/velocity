package grpc_test

// Coverage for finding I-02: the gRPC server must refuse to bind a cleartext
// listener in production unless an operator explicitly opts out via
// GRPC_INSECURE for a known-internal mTLS mesh.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/credentials"

	"github.com/velocitykode/velocity/grpc"
	"github.com/velocitykode/velocity/log"
)

// selfSignedTLSCreds returns server-side transport credentials backed by a
// freshly generated self-signed cert. Used to drive the "with creds" tests.
func selfSignedTLSCreds(t *testing.T) credentials.TransportCredentials {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "velocity-test"},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(1 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("x509 keypair: %v", err)
	}
	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{pair},
		MinVersion:   tls.VersionTLS12,
	})
}

func nullServerLogger(t *testing.T) log.Logger {
	t.Helper()
	l, err := log.NewLogger(log.LogConfig{Driver: "null"})
	if err != nil {
		t.Fatalf("null logger: %v", err)
	}
	return l
}

// TestServerBuild_ProductionRequiresTLS covers the core finding: production
// environment with no creds and no GRPC_INSECURE opt-out must fail Build.
func TestServerBuild_ProductionRequiresTLS(t *testing.T) {
	t.Setenv("GRPC_INSECURE", "")
	s := grpc.NewServer(
		grpc.WithPort("0"),
		grpc.WithEnvironment("production"),
		grpc.WithLogger(nullServerLogger(t)),
	)
	err := s.Build()
	if err == nil {
		t.Fatal("Build must refuse production start without TLS credentials")
	}
	if !strings.Contains(err.Error(), "velocity/grpc") {
		t.Errorf("error must be prefixed with velocity/grpc, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "TLS") {
		t.Errorf("error must mention TLS, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "production") {
		t.Errorf("error must mention production, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "GRPC_INSECURE") {
		t.Errorf("error must mention the GRPC_INSECURE opt-out, got %q", err.Error())
	}
}

// TestServerBuild_ProductionAllowsInsecureOptOut documents the escape hatch:
// operators running a known-internal mesh (sidecar-terminated TLS, mTLS mesh
// service mesh, etc.) can set GRPC_INSECURE=true and Build will not refuse.
func TestServerBuild_ProductionAllowsInsecureOptOut(t *testing.T) {
	t.Setenv("GRPC_INSECURE", "true")
	s := grpc.NewServer(
		grpc.WithPort("0"),
		grpc.WithEnvironment("production"),
		grpc.WithLogger(nullServerLogger(t)),
	)
	defer s.Stop()
	if err := s.Build(); err != nil {
		t.Fatalf("Build with GRPC_INSECURE=true must succeed in production, got %v", err)
	}
}

// TestServerBuild_ProductionAllowsWithCreds covers the canonical path: the
// caller explicitly attaches transport credentials via WithCreds.
func TestServerBuild_ProductionAllowsWithCreds(t *testing.T) {
	t.Setenv("GRPC_INSECURE", "")
	s := grpc.NewServer(
		grpc.WithPort("0"),
		grpc.WithEnvironment("production"),
		grpc.WithLogger(nullServerLogger(t)),
		grpc.WithCreds(selfSignedTLSCreds(t)),
	)
	defer s.Stop()
	if err := s.Build(); err != nil {
		t.Fatalf("Build with WithCreds must succeed in production, got %v", err)
	}
}

// TestServerBuild_DevWarnsWithoutCreds covers the dev ergonomics path: outside
// production the server still boots without creds, just with a warning.
func TestServerBuild_DevWarnsWithoutCreds(t *testing.T) {
	t.Setenv("GRPC_INSECURE", "")
	s := grpc.NewServer(
		grpc.WithPort("0"),
		grpc.WithEnvironment("development"),
		grpc.WithLogger(nullServerLogger(t)),
	)
	defer s.Stop()
	if err := s.Build(); err != nil {
		t.Fatalf("Build outside production should not fail with missing creds, got %v", err)
	}
}
