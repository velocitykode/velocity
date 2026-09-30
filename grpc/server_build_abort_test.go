package grpc_test

import (
	"net"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/grpc"
	"github.com/velocitykode/velocity/internal/testnet"
)

// valueListener is a listener whose dynamic value is not comparable: it
// is used by value and holds a slice.
type valueListener struct {
	net.Listener
	tags []string
}

// A panicking registration on a server built over a non-comparable
// listener still ends its Build: a later Build runs instead of returning
// ErrBuildInProgress for good.
func TestServerBuild_NonComparableListenerLeavesNoBuildInProgress(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	s := grpc.NewServer(grpc.WithListener(valueListener{Listener: testnet.Loopback(t), tags: []string{"a"}}),
		grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
	s.RegisterService(func(any) {
		if fail.Load() {
			panic("registration broke")
		}
	})
	stopOnCleanup(t, s)
	func() {
		defer func() { _ = recover() }()
		_ = s.Build()
	}()
	fail.Store(false)
	if err := s.Build(); err != nil {
		t.Fatalf("Build after the panic = %v, want nil", err)
	}
	if s.GRPCServer() == nil {
		t.Fatal("no server built")
	}
}
