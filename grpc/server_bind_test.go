package grpc

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/velocitykode/velocity/log"
)

func nullServer(t *testing.T, opts ...ServerOption) *Server {
	t.Helper()
	t.Setenv("GRPC_INSECURE", "true")
	logger, _ := log.NewLogger(log.LogConfig{Driver: "null"})
	return NewServer(append([]ServerOption{WithLogger(logger)}, opts...)...)
}

// TestBuild_DefaultBindsTCP confirms the legacy default still binds tcp on the
// port when no bind override is given.
func TestBuild_DefaultBindsTCP(t *testing.T) {
	s := nullServer(t, WithPort("0"))
	if err := s.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer s.Stop()
	if got := s.listener.Addr().Network(); got != "tcp" {
		t.Errorf("default network = %q, want tcp", got)
	}
}

// TestBuild_BindAddressLoopback binds 127.0.0.1 only, the posture a control API
// that must not be reachable off-host needs.
func TestBuild_BindAddressLoopback(t *testing.T) {
	s := nullServer(t, WithBindAddress("tcp", "127.0.0.1:0"))
	if err := s.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer s.Stop()
	addr := s.listener.Addr().String()
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Errorf("bound addr = %q, want 127.0.0.1:*", addr)
	}
}

// TestBuild_BindAddressUnix binds a unix-domain socket.
func TestBuild_BindAddressUnix(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "velvm.sock")
	s := nullServer(t, WithBindAddress("unix", sock))
	if err := s.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer s.Stop()
	if got := s.listener.Addr().Network(); got != "unix" {
		t.Errorf("network = %q, want unix", got)
	}
	if got := s.listener.Addr().String(); got != sock {
		t.Errorf("socket path = %q, want %q", got, sock)
	}
}

// TestBuild_ListenerTakesPrecedence proves WithListener wins over WithBindAddress
// and WithPort.
func TestBuild_ListenerTakesPrecedence(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	want := lis.Addr().String()
	s := nullServer(t,
		WithPort("50051"),
		WithBindAddress("tcp", "127.0.0.1:0"),
		WithListener(lis),
	)
	if err := s.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer s.Stop()
	if s.listener != lis {
		t.Error("WithListener did not take precedence")
	}
	if got := s.listener.Addr().String(); got != want {
		t.Errorf("adopted listener addr = %q, want %q", got, want)
	}
}

// TestBuild_ValidationFailureLeavesNoListener guards the ordering invariant: a
// fallible check (reflection-in-production) must fail BEFORE the listener is
// bound, so s.listener and s.grpcServer stay nil and a caller-supplied listener
// is never silently adopted (and thus never leaked on the failed build).
func TestBuild_ValidationFailureLeavesNoListener(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer lis.Close()
	s := nullServer(t,
		WithListener(lis),
		WithReflection(true),
		WithEnvironment("production"),
	)
	if err := s.Build(); err == nil {
		t.Fatal("expected Build to fail on reflection-in-production")
	}
	if s.listener != nil {
		t.Error("s.listener set after a failed Build")
	}
	if s.grpcServer != nil {
		t.Error("s.grpcServer set after a failed Build")
	}
}

// TestBuild_StopReleasesUnstartedListener proves a server that was Built but
// never Started releases its bound socket on Stop and resets to a rebuildable
// state (grpcServer must not outlive its listener).
func TestBuild_StopReleasesUnstartedListener(t *testing.T) {
	s := nullServer(t, WithBindAddress("tcp", "127.0.0.1:0"))
	if err := s.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	addr := s.listener.Addr().String()

	s.Stop()
	if s.listener != nil {
		t.Error("listener not released after Stop on an unstarted server")
	}
	if s.grpcServer != nil {
		t.Error("grpcServer not reset after Stop on an unstarted server")
	}

	// The freed address must be bindable again, and Build must actually rebuild
	// (not early-return on a stale grpcServer).
	s2 := nullServer(t, WithBindAddress("tcp", addr))
	if err := s2.Build(); err != nil {
		t.Fatalf("rebind freed addr: %v", err)
	}
	defer s2.Stop()
}

// TestServer_DoubleStopAfterRunningIsSafe guards the served-flag gating: once a
// server has actually served (StartAsync), a second Stop must NOT enter the
// built-but-unstarted cleanup branch and close/nil the listener the serve
// goroutine may still read. Under -race this fails if the branch is gated on
// !running instead of !served.
func TestServer_DoubleStopAfterRunningIsSafe(t *testing.T) {
	s := nullServer(t, WithBindAddress("tcp", "127.0.0.1:0"))
	if err := s.StartAsync(); err != nil {
		t.Fatalf("StartAsync: %v", err)
	}
	for i := 0; i < 200 && !s.IsRunning(); i++ {
		time.Sleep(time.Millisecond)
	}
	s.Stop()
	s.Stop() // idempotent: must be a no-op, never a race or panic
}

// TestBuild_BadBindAddressErrors surfaces a listen failure with the velocity
// prefix and the resolved target.
func TestBuild_BadBindAddressErrors(t *testing.T) {
	s := nullServer(t, WithBindAddress("tcp", "256.256.256.256:1"))
	err := s.Build()
	if err == nil {
		t.Fatal("expected Build to fail on an unresolvable bind address")
	}
	if !strings.Contains(err.Error(), "velocity/grpc") {
		t.Errorf("error not prefixed velocity/grpc: %q", err.Error())
	}
}
