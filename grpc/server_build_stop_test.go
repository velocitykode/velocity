package grpc_test

import (
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	grpcgo "google.golang.org/grpc"

	"github.com/velocitykode/velocity/grpc"
)

// buildBlockedStop starts a Build whose registration blocks, stops the
// server while it does, then lets the Build finish. It returns Build's
// error once both have returned.
func buildBlockedStop(t *testing.T, s *grpc.Server) error {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	s.RegisterService(func(any) {
		close(entered)
		<-release
	})
	built := make(chan error, 1)
	go func() { built <- s.Build() }()
	<-entered
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		s.Stop()
	}()
	time.Sleep(50 * time.Millisecond) // let Stop reach the server
	close(release)
	var err error
	select {
	case err = <-built:
	case <-time.After(3 * time.Second):
		t.Fatal("Build did not return")
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop did not return")
	}
	return err
}

// A Stop during a Build in progress leaves nothing live behind: the Build
// publishes no server, and the listener it bound is closed.
func TestServerStop_DuringBuildReleasesItsListener(t *testing.T) {
	port := freePort(t)
	s := grpc.NewServer(grpc.WithBindAddress("tcp", "127.0.0.1:"+port), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
	stopOnCleanup(t, s)
	_ = buildBlockedStop(t, s)
	if c, err := net.Dial("tcp", "127.0.0.1:"+port); err == nil {
		_ = c.Close()
		t.Error("the Build's listener is still open after Stop")
	}
	if s.GRPCServer() != nil || s.Address() != "" {
		t.Errorf("server %v at %q after Stop: want nothing published", s.GRPCServer(), s.Address())
	}
}

// A caller-supplied listener is closed by a Stop during Build too, as
// Stop closes it on a built server.
func TestServerStop_DuringBuildClosesASuppliedListener(t *testing.T) {
	lis := &closeTracker{Listener: loopback(t)}
	s := grpc.NewServer(grpc.WithListener(lis), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
	stopOnCleanup(t, s)
	_ = buildBlockedStop(t, s)
	if !lis.closed.Load() {
		t.Error("supplied listener still open after Stop")
	}
	if s.GRPCServer() != nil {
		t.Error("server published after Stop")
	}
}

// The Build a Stop interrupted returns grpc-go's ErrServerStopped, and a
// later Build constructs the server afresh.
func TestServerBuild_StoppedBuildReturnsErrServerStopped(t *testing.T) {
	s := grpc.NewServer(grpc.WithBindAddress("tcp", "127.0.0.1:0"), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
	stopOnCleanup(t, s)
	if err := buildBlockedStop(t, s); !errors.Is(err, grpcgo.ErrServerStopped) {
		t.Fatalf("Build = %v, want ErrServerStopped", err)
	}
	s2 := grpc.NewServer(grpc.WithBindAddress("tcp", "127.0.0.1:0"), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
	stopOnCleanup(t, s2)
	if err := s2.Build(); err != nil || s2.GRPCServer() == nil {
		t.Fatalf("Build = %v, server %v: want a built server", err, s2.GRPCServer())
	}
}

// Builds and Stops racing on many goroutines leave no listener open once
// a last Stop has run, and every Build returns nil, ErrBuildInProgress or
// ErrServerStopped. Under -race there is no data race.
func TestServerStop_RacingBuildsLeaveNoListenerOpen(t *testing.T) {
	for range 50 {
		lis := &closeTracker{Listener: loopback(t)}
		s := grpc.NewServer(grpc.WithListener(lis), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
		s.RegisterService(func(any) {})
		done := make(chan struct{})
		for i := range 8 {
			go func() {
				defer func() { done <- struct{}{} }()
				if i%2 == 0 {
					s.Stop()
					return
				}
				err := s.Build()
				if err != nil && !errors.Is(err, grpc.ErrBuildInProgress) && !errors.Is(err, grpcgo.ErrServerStopped) {
					t.Errorf("Build: %v", err)
				}
			}()
		}
		for range 8 {
			<-done
		}
		s.Stop()
		if !lis.closed.Load() {
			t.Fatal("listener open after the last Stop")
		}
	}
}
