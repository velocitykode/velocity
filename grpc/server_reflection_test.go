package grpc

import (
	"strings"
	"testing"

	"github.com/velocitykode/velocity/log"
)

// TestBuild_RefusesReflectionInProduction covers Task 8a: enabling reflection
// in production must be a hard failure, not a silent downgrade. The TLS guard
// (I-02) fires earlier than the reflection guard, so the test opts out of it
// via GRPC_INSECURE so the reflection check is the one exercised here.
func TestBuild_RefusesReflectionInProduction(t *testing.T) {
	t.Setenv("GRPC_INSECURE", "true")
	logger, _ := log.NewLogger(log.LogConfig{Driver: "null"})
	s := NewServer(
		WithPort("0"),
		WithReflection(true),
		WithEnvironment("production"),
		WithLogger(logger),
	)

	err := s.Build()
	if err == nil {
		t.Fatal("expected Build to fail when reflection is enabled in production")
	}
	if !strings.Contains(err.Error(), "velocity/grpc") {
		t.Errorf("error should be prefixed with velocity/grpc, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "reflection") {
		t.Errorf("error should mention reflection, got %q", err.Error())
	}
}

// TestBuild_RefusesReflectionInStaging ensures the production guard folds
// "staging" into the locked-down branch. Sweep 3 of the 1.0 readiness work
// updated the gRPC guards to route through contract.IsProductionEnv, which
// classifies staging as production: a typo'd APP_ENV must not silently
// re-enable reflection.
func TestBuild_RefusesReflectionInStaging(t *testing.T) {
	t.Setenv("GRPC_INSECURE", "true")
	logger, _ := log.NewLogger(log.LogConfig{Driver: "null"})
	s := NewServer(
		WithPort("0"),
		WithReflection(true),
		WithEnvironment("staging"),
		WithLogger(logger),
	)
	defer s.Stop()

	err := s.Build()
	if err == nil {
		t.Fatal("expected Build to fail when reflection is enabled with APP_ENV=staging")
	}
	if !strings.Contains(err.Error(), "reflection") {
		t.Errorf("error should mention reflection, got %q", err.Error())
	}
}

// TestBuild_AllowsReflectionInDevelopment ensures dev ergonomics still work
// after the staging tightening above. "development" is the unambiguous non-
// prod label and reflection is welcome there.
func TestBuild_AllowsReflectionInDevelopment(t *testing.T) {
	logger, _ := log.NewLogger(log.LogConfig{Driver: "null"})
	s := NewServer(
		WithPort("0"),
		WithReflection(true),
		WithEnvironment("development"),
		WithLogger(logger),
	)
	defer s.Stop()

	if err := s.Build(); err != nil {
		t.Fatalf("development Build: unexpected error %v", err)
	}
}

// TestBuild_RequiresNonNilLogger covers the second half of Task 8a.
func TestBuild_RequiresNonNilLogger(t *testing.T) {
	// NewServer installs a default console logger; clear it to exercise the
	// Build() nil-logger guard.
	s := NewServer(WithPort("0"))
	s.logger = nil

	err := s.Build()
	if err == nil {
		t.Fatal("expected Build to fail with nil logger")
	}
	if !strings.Contains(err.Error(), "logger is required") {
		t.Errorf("expected 'logger is required' in message, got %q", err.Error())
	}
}
