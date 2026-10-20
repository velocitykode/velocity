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
	l, err := log.NewLogger(log.Config{Driver: "null"})
	if err != nil {
		t.Fatalf("null logger: %v", err)
	}
	return l
}

// TestServerBuild_ProductionRequiresTLS covers the core finding: production
// environment with no creds and no Insecure (GRPC_INSECURE) opt-out must fail Build.
func TestServerBuild_ProductionRequiresTLS(t *testing.T) {
	s := grpc.NewServer(grpc.Config{Environment: "test"},
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
// service mesh, etc.) can set Config.Insecure (GRPC_INSECURE=true) and Build
// will not refuse.
func TestServerBuild_ProductionAllowsInsecureOptOut(t *testing.T) {
	s := grpc.NewServer(grpc.Config{Environment: "test", Insecure: true},
		grpc.WithPort("0"),
		grpc.WithEnvironment("production"),
		grpc.WithLogger(nullServerLogger(t)),
	)
	defer s.Stop()
	if err := s.Build(); err != nil {
		t.Fatalf("Build with Config.Insecure must succeed in production, got %v", err)
	}
}

// TestServerBuild_ProductionAllowsWithCreds covers the canonical path: the
// caller explicitly attaches transport credentials via WithCreds.
func TestServerBuild_ProductionAllowsWithCreds(t *testing.T) {
	s := grpc.NewServer(grpc.Config{Environment: "test"},
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
	s := grpc.NewServer(grpc.Config{Environment: "test"},
		grpc.WithPort("0"),
		grpc.WithEnvironment("development"),
		grpc.WithLogger(nullServerLogger(t)),
	)
	defer s.Stop()
	if err := s.Build(); err != nil {
		t.Fatalf("Build outside production should not fail with missing creds, got %v", err)
	}
}

// TestServerBuild_ProductionFromConfigEnvironment covers the config path for
// the environment: Config.Environment, not only WithEnvironment, drives the
// production TLS guard.
func TestServerBuild_ProductionFromConfigEnvironment(t *testing.T) {
	for _, env := range []string{"production", "prod", "staging"} {
		s := grpc.NewServer(grpc.Config{Environment: env},
			grpc.WithPort("0"),
			grpc.WithLogger(nullServerLogger(t)),
		)
		if err := s.Build(); err == nil {
			s.Stop()
			t.Errorf("Environment=%q: Build must refuse a production start without TLS credentials", env)
		}
	}
}

// TestServerBuild_UnsetEnvironmentIsNonProduction pins the unset-environment
// rule: a zero Config.Environment is non-production (contract.IsProductionEnv
// of ""), so the server boots without creds.
func TestServerBuild_UnsetEnvironmentIsNonProduction(t *testing.T) {
	s := grpc.NewServer(grpc.Config{Environment: "test"},
		grpc.WithPort("0"),
		grpc.WithLogger(nullServerLogger(t)),
	)
	defer s.Stop()
	if err := s.Build(); err != nil {
		t.Fatalf("Build with an unset environment must not fail, got %v", err)
	}
}

// TestServerBuild_WithEnvironmentOverridesConfig confirms the option wins over
// Config.Environment in both directions.
func TestServerBuild_WithEnvironmentOverridesConfig(t *testing.T) {
	dev := grpc.NewServer(grpc.Config{Environment: "production"},
		grpc.WithPort("0"),
		grpc.WithEnvironment("development"),
		grpc.WithLogger(nullServerLogger(t)),
	)
	defer dev.Stop()
	if err := dev.Build(); err != nil {
		t.Fatalf("WithEnvironment(development) must override Config.Environment=production, got %v", err)
	}

	prod := grpc.NewServer(grpc.Config{Environment: "development"},
		grpc.WithPort("0"),
		grpc.WithEnvironment("production"),
		grpc.WithLogger(nullServerLogger(t)),
	)
	if err := prod.Build(); err == nil {
		prod.Stop()
		t.Fatal("WithEnvironment(production) must override Config.Environment=development and refuse without TLS")
	}
}
