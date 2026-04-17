package grpc

import (
	"strings"
	"testing"

	"github.com/velocitykode/velocity/log"
)

// TestBuild_RefusesReflectionInProduction covers Task 8a: enabling reflection
// in production must be a hard failure, not a silent downgrade.
func TestBuild_RefusesReflectionInProduction(t *testing.T) {
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

// TestBuild_AllowsReflectionInStaging ensures the guard is targeted and does
// not regress the staging / dev ergonomics.
func TestBuild_AllowsReflectionInStaging(t *testing.T) {
	logger, _ := log.NewLogger(log.LogConfig{Driver: "null"})
	s := NewServer(
		WithPort("0"),
		WithReflection(true),
		WithEnvironment("staging"),
		WithLogger(logger),
	)
	defer s.Stop()

	if err := s.Build(); err != nil {
		t.Fatalf("staging Build: unexpected error %v", err)
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
