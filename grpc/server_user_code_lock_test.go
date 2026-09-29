package grpc_test

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc"
	"github.com/velocitykode/velocity/grpc/interceptors"
)

// within fails the test when fn does not return within d: a deadlock
// surfaces as a failure, not a hung suite.
func within(t *testing.T, d time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not return within %v: user code ran under the server's lock", what, d)
	}
}

// stopOnCleanup stops s when the test ends, without hanging the suite
// when a deadlocked server cannot stop.
func stopOnCleanup(t *testing.T, s *grpc.Server) {
	t.Cleanup(func() {
		done := make(chan struct{})
		go func() {
			defer close(done)
			s.Stop()
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	})
}

// loopback returns a listener on an ephemeral loopback port.
func loopback(t *testing.T) net.Listener {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return lis
}

// reentrantLogger calls back into the server from every line it writes.
type reentrantLogger struct {
	server *atomic.Pointer[grpc.Server]
	lines  atomic.Int32
}

func (l *reentrantLogger) touch() {
	l.lines.Add(1)
	if s := l.server.Load(); s != nil {
		_ = s.Address()
		_ = s.IsRunning()
	}
}
func (l *reentrantLogger) Debug(string, ...any)        { l.touch() }
func (l *reentrantLogger) Info(string, ...any)         { l.touch() }
func (l *reentrantLogger) Warn(string, ...any)         { l.touch() }
func (l *reentrantLogger) Error(string, ...any)        { l.touch() }
func (l *reentrantLogger) Fatal(string, ...any)        { l.touch() }
func (l *reentrantLogger) With(...any) contract.Logger { return l }

// Build runs application code (a CallOption, a registration function, the
// logger's warnings) without holding the server's lock: each may call a
// server accessor, and Build still returns.
func TestServerBuild_UserCodeMayCallTheServer(t *testing.T) {
	var ref atomic.Pointer[grpc.Server]
	logger := &reentrantLogger{server: &ref}
	var optionRan, registrationRan atomic.Bool
	s := grpc.NewServer(
		grpc.WithListener(loopback(t)),
		grpc.WithLogger(logger),
		grpc.WithReflection(true),
		grpc.WithEnvironment("development"),
		grpc.WithCallOptions(func(*interceptors.CallConfig) {
			optionRan.Store(true)
			_ = ref.Load().Address()
		}),
	)
	s.RegisterService(func(any) {
		registrationRan.Store(true)
		_ = ref.Load().IsRunning()
		_ = ref.Load().GRPCServer()
	})
	ref.Store(s)
	stopOnCleanup(t, s)

	var err error
	within(t, 2*time.Second, "Build", func() { err = s.Build() })
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !optionRan.Load() || !registrationRan.Load() {
		t.Errorf("option ran %v, registration ran %v: want both", optionRan.Load(), registrationRan.Load())
	}
	if s.GRPCServer() == nil || s.Address() == "" {
		t.Errorf("server %v at %q: want a built server with its address", s.GRPCServer(), s.Address())
	}
	// TLS, unauthenticated-surface and reflection warnings.
	if got := logger.lines.Load(); got != 3 {
		t.Errorf("warnings = %d, want 3", got)
	}
}

// A Build called from inside a Build in progress (a registration that
// builds, say) returns ErrBuildInProgress at once instead of waiting on
// itself, and the outer Build completes, running the application code
// once.
func TestServerBuild_ReentrantBuildReturnsAnError(t *testing.T) {
	var ref atomic.Pointer[grpc.Server]
	var inner error
	var registrations atomic.Int32
	s := grpc.NewServer(grpc.WithListener(loopback(t)), grpc.WithLogger(&reentrantLogger{server: &ref}))
	s.RegisterService(func(any) {
		registrations.Add(1)
		inner = ref.Load().Build()
	})
	ref.Store(s)
	stopOnCleanup(t, s)

	var err error
	within(t, 2*time.Second, "Build", func() { err = s.Build() })
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !errors.Is(inner, grpc.ErrBuildInProgress) {
		t.Errorf("inner Build = %v, want ErrBuildInProgress", inner)
	}
	if registrations.Load() != 1 {
		t.Errorf("registrations ran %d times, want 1", registrations.Load())
	}
	if err := s.Build(); err != nil {
		t.Errorf("Build after Build = %v, want nil (already built)", err)
	}
}

// Many concurrent Builds construct one server: each returns nil or
// ErrBuildInProgress, the application code runs once, and the server is
// built afterwards. Under -race there is no data race.
func TestServerBuild_ConcurrentBuildsBuildOnce(t *testing.T) {
	for range 20 {
		var registrations atomic.Int32
		s := grpc.NewServer(grpc.WithListener(loopback(t)), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
		s.RegisterService(func(any) { registrations.Add(1) })
		var wg sync.WaitGroup
		for range 16 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := s.Build(); err != nil && !errors.Is(err, grpc.ErrBuildInProgress) {
					t.Errorf("Build: %v", err)
				}
				_ = s.Address()
				_ = s.GRPCServer()
			}()
		}
		wg.Wait()
		if registrations.Load() != 1 {
			t.Fatalf("registrations ran %d times, want 1", registrations.Load())
		}
		if err := s.Build(); err != nil || s.GRPCServer() == nil {
			t.Fatalf("Build = %v, server %v: want a built server", err, s.GRPCServer())
		}
		s.Stop()
	}
}

// A registration function that panics leaves no Build in progress: the
// panic reaches the caller as before, the listener Build bound is
// released, and a later Build can run.
func TestServerBuild_PanickingRegistrationLeavesNoBuildInProgress(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	s := grpc.NewServer(grpc.WithPort("0"), grpc.WithBindAddress("tcp", "127.0.0.1:0"), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
	s.RegisterService(func(any) {
		if fail.Load() {
			panic("registration broke")
		}
	})
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("Build returned; want the registration's panic")
			}
		}()
		_ = s.Build()
	}()
	if s.GRPCServer() != nil || s.Address() != "" {
		t.Fatalf("server %v at %q after a failed Build: want nothing published", s.GRPCServer(), s.Address())
	}
	fail.Store(false)
	if err := s.Build(); err != nil {
		t.Fatalf("Build after the panic = %v, want nil", err)
	}
	s.Stop()
}
