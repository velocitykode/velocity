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

// buildingListener runs onClose, once armed, from its Close before it
// closes the socket.
type buildingListener struct {
	net.Listener
	armed   atomic.Bool
	onClose func()
	closed  atomic.Bool
}

func (l *buildingListener) Close() error {
	if l.armed.CompareAndSwap(true, false) {
		l.onClose()
	}
	l.closed.Store(true)
	return l.Listener.Close()
}

// closeRebuild arms lis to call Build on s from its Close and returns
// where Build's error lands.
func closeRebuild(lis *buildingListener, s *atomic.Pointer[grpc.Server]) *error {
	var err error
	lis.onClose = func() { err = s.Load().Build() }
	lis.armed.Store(true)
	return &err
}

// A Build a Stop ended keeps its Build in progress until it has closed
// the caller's listener: a Build started from that listener's Close gets
// ErrBuildInProgress, and nothing is published over the closed listener.
func TestServerBuild_StoppedAbortHoldsTheBuildUntilItsCloseEnds(t *testing.T) {
	var ref atomic.Pointer[grpc.Server]
	lis := &buildingListener{Listener: loopback(t)}
	s := grpc.NewServer(grpc.WithListener(lis), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
	ref.Store(s)
	stopOnCleanup(t, s)
	nested := closeRebuild(lis, &ref)
	var stopOnce atomic.Bool
	s.RegisterService(func(any) {
		if stopOnce.CompareAndSwap(false, true) { // only the first Build is stopped
			ref.Load().Stop()
		}
	})

	var err error
	within(t, 2*time.Second, "Build", func() { err = s.Build() })
	if !errors.Is(err, grpcgo.ErrServerStopped) {
		t.Fatalf("Build = %v, want ErrServerStopped", err)
	}
	if !errors.Is(*nested, grpc.ErrBuildInProgress) {
		t.Errorf("Build from the listener's Close = %v, want ErrBuildInProgress", *nested)
	}
	if s.GRPCServer() != nil || s.Address() != "" {
		t.Errorf("server %v at %q: want nothing published over the closed listener", s.GRPCServer(), s.Address())
	}
}

// The same holds for the listener a Stop releases from a built but never
// served server.
func TestServerStop_UnservedCloseHoldsOffABuild(t *testing.T) {
	var ref atomic.Pointer[grpc.Server]
	lis := &buildingListener{Listener: loopback(t)}
	s := grpc.NewServer(grpc.WithListener(lis), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
	ref.Store(s)
	stopOnCleanup(t, s)
	if err := s.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	nested := closeRebuild(lis, &ref)
	within(t, 2*time.Second, "Stop", s.Stop)
	if !errors.Is(*nested, grpc.ErrBuildInProgress) {
		t.Errorf("Build from the listener's Close = %v, want ErrBuildInProgress", *nested)
	}
	if s.GRPCServer() != nil || s.Address() != "" {
		t.Errorf("server %v at %q: want nothing published over the closed listener", s.GRPCServer(), s.Address())
	}
}

// A Build racing a stopped abort's blocking Close gets ErrBuildInProgress
// until the Close has ended.
func TestServerBuild_RacingTheStoppedAbortsCloseIsHeldOff(t *testing.T) {
	var ref atomic.Pointer[grpc.Server]
	lis := &buildingListener{Listener: loopback(t)}
	s := grpc.NewServer(grpc.WithListener(lis), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
	ref.Store(s)
	stopOnCleanup(t, s)
	entered, release := make(chan struct{}), make(chan struct{})
	lis.onClose = func() {
		close(entered)
		<-release
	}
	lis.armed.Store(true)
	var stopOnce atomic.Bool
	s.RegisterService(func(any) {
		if stopOnce.CompareAndSwap(false, true) { // only the first Build is stopped
			ref.Load().Stop()
		}
	})

	built := make(chan error, 1)
	go func() { built <- s.Build() }()
	<-entered
	if err := s.Build(); !errors.Is(err, grpc.ErrBuildInProgress) {
		t.Errorf("Build during the abort's Close = %v, want ErrBuildInProgress", err)
	}
	close(release)
	select {
	case <-built:
	case <-time.After(2 * time.Second):
		t.Fatal("aborted Build did not return")
	}
	if s.GRPCServer() != nil {
		t.Error("server published over the closed listener")
	}
}
