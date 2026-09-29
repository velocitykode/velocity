package grpc_test

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc"
	"github.com/velocitykode/velocity/grpc/grpcevents"
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

// startHealth starts s serving the health service and returns a client.
func startHealth(t *testing.T, s *grpc.Server) grpc_health_v1.HealthClient {
	t.Helper()
	s.RegisterService(func(srv any) {
		grpc_health_v1.RegisterHealthServer(srv.(*grpcgo.Server), health.NewServer())
	})
	if err := s.StartAsync(); err != nil {
		t.Fatalf("StartAsync: %v", err)
	}
	stopOnCleanup(t, s)
	conn, err := grpcgo.NewClient(s.Address(), grpcgo.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return grpc_health_v1.NewHealthClient(conn)
}

// GracefulStop waits for in-flight calls without holding the server's
// lock: a call that reads a server accessor while the server drains
// completes, and so does the stop.
func TestServerGracefulStop_InFlightCallMayCallTheServer(t *testing.T) {
	var ref atomic.Pointer[grpc.Server]
	entered, stopping := make(chan struct{}), make(chan struct{})
	s := grpc.NewServer(grpc.WithListener(loopback(t)), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
	s.Use(func(ctx context.Context, req any, _ *grpcgo.UnaryServerInfo, h grpcgo.UnaryHandler) (any, error) {
		close(entered)
		<-stopping
		time.Sleep(50 * time.Millisecond) // let GracefulStop reach its drain
		_ = ref.Load().Address()
		_ = ref.Load().IsRunning()
		return h(ctx, req)
	})
	ref.Store(s)
	client := startHealth(t, s)

	callDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := client.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
		callDone <- err
	}()
	<-entered
	close(stopping)
	within(t, 3*time.Second, "GracefulStop", s.GracefulStop)
	if err := <-callDone; err != nil {
		t.Errorf("in-flight call = %v, want it to complete", err)
	}
	if s.IsRunning() {
		t.Error("server still running after GracefulStop")
	}
}

// Shutdown returns the ctx error at its deadline while a handler that
// ignores its context still runs: the drain no longer holds the return,
// and the forced stop it starts neither holds the server's lock nor
// blocks the return. The handler, released afterwards, ends normally.
func TestServerShutdown_ReturnsAtItsDeadlineWithAHangingCall(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	handlerDone := make(chan struct{})
	s := grpc.NewServer(grpc.WithListener(loopback(t)), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
	s.Use(func(ctx context.Context, req any, _ *grpcgo.UnaryServerInfo, h grpcgo.UnaryHandler) (any, error) {
		defer close(handlerDone)
		close(entered)
		<-release // ignores ctx
		return h(ctx, req)
	})
	client := startHealth(t, s)
	go func() {
		_, _ = client.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	}()
	<-entered

	const deadline = 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	began := time.Now()
	var err error
	within(t, 2*time.Second, "Shutdown", func() { err = s.Shutdown(ctx) })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Shutdown = %v, want the deadline", err)
	}
	if took := time.Since(began); took > deadline+500*time.Millisecond {
		t.Errorf("Shutdown took %v, want about its %v deadline", took, deadline)
	}
	close(release)
	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("released handler did not end")
	}
}

// A handler that honours its context ends through the forced stop's
// transport close once Shutdown's deadline passes.
func TestServerShutdown_DeadlineCancelsAHandlerThatHonoursItsContext(t *testing.T) {
	entered, handlerDone := make(chan struct{}), make(chan struct{})
	s := grpc.NewServer(grpc.WithListener(loopback(t)), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
	s.Use(func(ctx context.Context, req any, _ *grpcgo.UnaryServerInfo, h grpcgo.UnaryHandler) (any, error) {
		defer close(handlerDone)
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	client := startHealth(t, s)
	go func() {
		_, _ = client.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	}()
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	var err error
	within(t, 2*time.Second, "Shutdown", func() { err = s.Shutdown(ctx) })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Shutdown = %v, want the deadline", err)
	}
	select {
	case <-handlerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("handler honouring its context was not cancelled by the forced stop")
	}
}

// stoppedCounter counts ServerStopped events.
type stoppedCounter struct{ n atomic.Int32 }

func (c *stoppedCounter) dispatch(_ context.Context, ev any) error {
	if _, ok := ev.(*grpcevents.ServerStopped); ok {
		c.n.Add(1)
	}
	return nil
}

// ServerStopped is dispatched exactly once however the stops race: a
// Shutdown whose deadline forces the stop, a Stop and a GracefulStop, all
// at once, with a call in flight.
func TestServerStop_RacingStopsDispatchServerStoppedOnce(t *testing.T) {
	for range 10 {
		entered, release := make(chan struct{}), make(chan struct{})
		var enteredOnce sync.Once
		s := grpc.NewServer(grpc.WithListener(loopback(t)), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
		counter := &stoppedCounter{}
		s.SetEventDispatcher(counter.dispatch)
		s.Use(func(ctx context.Context, req any, _ *grpcgo.UnaryServerInfo, h grpcgo.UnaryHandler) (any, error) {
			enteredOnce.Do(func() { close(entered) })
			select {
			case <-release:
			case <-ctx.Done():
			}
			return h(ctx, req)
		})
		client := startHealth(t, s)
		go func() {
			_, _ = client.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
		}()
		<-entered

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		var wg sync.WaitGroup
		for _, stop := range []func(){
			func() { _ = s.Shutdown(ctx) },
			s.Stop,
			s.GracefulStop,
		} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				stop()
			}()
		}
		time.Sleep(100 * time.Millisecond)
		close(release)
		within(t, 3*time.Second, "the racing stops", wg.Wait)
		cancel()
		if got := counter.n.Load(); got != 1 {
			t.Fatalf("ServerStopped dispatched %d times, want 1", got)
		}
	}
}

// reentrantListener calls back into the server when it is closed or asked
// for its address.
type reentrantListener struct {
	net.Listener
	server *atomic.Pointer[grpc.Server]
	closed atomic.Bool
}

func (l *reentrantListener) Close() error {
	if s := l.server.Load(); s != nil {
		_ = s.IsRunning()
	}
	l.closed.Store(true)
	return l.Listener.Close()
}

// Stop releases a caller-supplied listener of a built but never served
// server without holding the server's lock: a listener whose Close calls
// the server still lets Stop return.
func TestServerStop_ListenerCloseMayCallTheServer(t *testing.T) {
	for name, stop := range map[string]func(*grpc.Server){"Stop": (*grpc.Server).Stop, "GracefulStop": (*grpc.Server).GracefulStop} {
		t.Run(name, func(t *testing.T) {
			var ref atomic.Pointer[grpc.Server]
			lis := &reentrantListener{Listener: loopback(t), server: &ref}
			s := grpc.NewServer(grpc.WithListener(lis), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
			ref.Store(s)
			if err := s.Build(); err != nil {
				t.Fatalf("Build: %v", err)
			}
			within(t, 2*time.Second, name, func() { stop(s) })
			if !lis.closed.Load() {
				t.Error("listener not closed")
			}
		})
	}
}
