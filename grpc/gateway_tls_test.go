package grpc_test

// Coverage for finding I-01: gateway must not silently default to cleartext
// in production, and must hard-fail on bad CA / cert files instead of silently
// downgrading to system roots or no client cert.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/velocitykode/velocity/grpc"
	"github.com/velocitykode/velocity/log"
)

// writeSelfSignedCertAndKey writes a self-signed cert and matching key to the
// given paths. Used to verify the mTLS happy path without standing up a CA.
func writeSelfSignedCertAndKey(t *testing.T, certPath, keyPath string) {
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
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
}

// nullLogger returns a logger that discards everything so the warn path
// emitted by the non-production fallback does not pollute test output.
func nullLogger(t *testing.T) log.Logger {
	t.Helper()
	l, err := log.NewLogger(log.LogConfig{Driver: "null"})
	if err != nil {
		t.Fatalf("null logger: %v", err)
	}
	return l
}

// TestGatewayBuild_ProductionRefusesMissingCreds covers case (a): production
// environment with no credentials must hard-fail. The old behaviour was a
// silent cleartext default that leaks every Authorization header.
func TestGatewayBuild_ProductionRefusesMissingCreds(t *testing.T) {
	g := grpc.NewGateway(
		grpc.GatewayWithGRPCEndpoint("localhost:50051"),
		grpc.GatewayWithEnvironment("production"),
		grpc.GatewayWithLogger(nullLogger(t)),
	)
	err := g.Build(context.Background())
	if err == nil {
		t.Fatal("Build must refuse production start without configured credentials")
	}
	if !strings.Contains(err.Error(), "velocity/grpc") {
		t.Errorf("error must be prefixed with velocity/grpc, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "production") {
		t.Errorf("error must mention production, got %q", err.Error())
	}
}

// TestGatewayBuild_ProductionAllowsExplicitInsecure documents the opt-out:
// an operator running a known-internal cleartext mesh can call
// GatewayWithInsecure() and Build will not refuse.
func TestGatewayBuild_ProductionAllowsExplicitInsecure(t *testing.T) {
	g := grpc.NewGateway(
		grpc.GatewayWithGRPCEndpoint("localhost:50051"),
		grpc.GatewayWithEnvironment("production"),
		grpc.GatewayWithInsecure(),
		grpc.GatewayWithLogger(nullLogger(t)),
	)
	if err := g.Build(context.Background()); err != nil {
		t.Fatalf("Build with explicit insecure opt-in must succeed in production, got %v", err)
	}
}

// TestGatewayBuild_DevDefaultsToInsecureWithWarning covers the dev ergonomics
// path: outside production the gateway still boots with cleartext but the
// caller is warned. We just assert no error here.
func TestGatewayBuild_DevDefaultsToInsecureWithWarning(t *testing.T) {
	g := grpc.NewGateway(
		grpc.GatewayWithGRPCEndpoint("localhost:50051"),
		grpc.GatewayWithEnvironment("development"),
		grpc.GatewayWithLogger(nullLogger(t)),
	)
	if err := g.Build(context.Background()); err != nil {
		t.Fatalf("Build outside production should not fail with missing creds, got %v", err)
	}
}

// TestGatewayWithTransportConfig_MissingCAFile covers case (b): if the operator
// references a CA cert path that does not exist, Build must fail rather than
// silently dropping back to the system root CA pool.
func TestGatewayWithTransportConfig_MissingCAFile(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "client.pem")
	keyPath := filepath.Join(dir, "client.key")
	writeSelfSignedCertAndKey(t, certPath, keyPath)

	g := grpc.NewGateway(
		grpc.GatewayWithGRPCEndpoint("localhost:50051"),
		grpc.GatewayWithLogger(nullLogger(t)),
		grpc.GatewayWithTransportConfig(grpc.GatewayTransportConfig{
			TLSCert: certPath,
			TLSKey:  keyPath,
			CACert:  filepath.Join(dir, "does-not-exist.pem"),
		}),
	)
	err := g.Build(context.Background())
	if err == nil {
		t.Fatal("Build must fail when the configured CA cert file is missing")
	}
	if !strings.Contains(err.Error(), "CA cert") {
		t.Errorf("error must mention CA cert, got %q", err.Error())
	}
}

// TestGatewayWithTransportConfig_MalformedCAPEM covers case (c): a CA cert
// file that exists but contains no valid PEM blocks must fail Build.
func TestGatewayWithTransportConfig_MalformedCAPEM(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "client.pem")
	keyPath := filepath.Join(dir, "client.key")
	writeSelfSignedCertAndKey(t, certPath, keyPath)

	badCA := filepath.Join(dir, "bad-ca.pem")
	if err := os.WriteFile(badCA, []byte("not a certificate"), 0o600); err != nil {
		t.Fatalf("write bad CA: %v", err)
	}

	g := grpc.NewGateway(
		grpc.GatewayWithGRPCEndpoint("localhost:50051"),
		grpc.GatewayWithLogger(nullLogger(t)),
		grpc.GatewayWithTransportConfig(grpc.GatewayTransportConfig{
			TLSCert: certPath,
			TLSKey:  keyPath,
			CACert:  badCA,
		}),
	)
	err := g.Build(context.Background())
	if err == nil {
		t.Fatal("Build must fail when the configured CA cert PEM cannot be parsed")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error must mention parse failure, got %q", err.Error())
	}
}

// TestGatewayWithTransportConfig_MissingClientCert covers the mTLS branch: a
// cert path that does not exist must fail Build rather than fall through to a
// no-client-cert TLS handshake.
func TestGatewayWithTransportConfig_MissingClientCert(t *testing.T) {
	dir := t.TempDir()
	g := grpc.NewGateway(
		grpc.GatewayWithGRPCEndpoint("localhost:50051"),
		grpc.GatewayWithLogger(nullLogger(t)),
		grpc.GatewayWithTransportConfig(grpc.GatewayTransportConfig{
			TLSCert: filepath.Join(dir, "missing.pem"),
			TLSKey:  filepath.Join(dir, "missing.key"),
		}),
	)
	err := g.Build(context.Background())
	if err == nil {
		t.Fatal("Build must fail when the configured client cert/key is missing")
	}
	if !strings.Contains(err.Error(), "client cert") {
		t.Errorf("error must mention client cert, got %q", err.Error())
	}
}

// TestGatewayWithTransportConfig_ValidCertAndCA covers case (d): a valid
// client cert/key pair plus a valid CA cert must build cleanly.
func TestGatewayWithTransportConfig_ValidCertAndCA(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "client.pem")
	keyPath := filepath.Join(dir, "client.key")
	writeSelfSignedCertAndKey(t, certPath, keyPath)

	// Reuse the self-signed cert as a CA. It is a CA in the generated
	// template, so it parses as a valid PEM block.
	g := grpc.NewGateway(
		grpc.GatewayWithGRPCEndpoint("localhost:50051"),
		grpc.GatewayWithLogger(nullLogger(t)),
		grpc.GatewayWithTransportConfig(grpc.GatewayTransportConfig{
			TLSCert: certPath,
			TLSKey:  keyPath,
			CACert:  certPath,
		}),
	)
	if err := g.Build(context.Background()); err != nil {
		t.Fatalf("Build with valid cert/key/CA must succeed, got %v", err)
	}
}

// TestGatewayWithTransportConfig_ValidCertNoCA covers the case where the
// operator only configures a client cert without pinning a CA. The system
// root pool is used for server verification.
func TestGatewayWithTransportConfig_ValidCertNoCA(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "client.pem")
	keyPath := filepath.Join(dir, "client.key")
	writeSelfSignedCertAndKey(t, certPath, keyPath)

	g := grpc.NewGateway(
		grpc.GatewayWithGRPCEndpoint("localhost:50051"),
		grpc.GatewayWithLogger(nullLogger(t)),
		grpc.GatewayWithEnvironment("production"),
		grpc.GatewayWithTransportConfig(grpc.GatewayTransportConfig{
			TLSCert: certPath,
			TLSKey:  keyPath,
		}),
	)
	if err := g.Build(context.Background()); err != nil {
		t.Fatalf("Build with valid cert/key (no CA pin) must succeed, got %v", err)
	}
}
