package grpc

import (
	"testing"

	"github.com/velocitykode/velocity/grpc/interceptors"
)

// TestUseAllAcceptsInterceptorPairs pins that UseAll takes the pairs returned
// by the interceptors package directly (InterceptorPair is an alias) and that
// both the unary and stream interceptor slices grow by one per pair.
func TestUseAllAcceptsInterceptorPairs(t *testing.T) {
	s := NewServer(WithPort("0"))

	beforeUnary := len(s.unaryInterceptors)
	beforeStream := len(s.streamInterceptors)

	s.UseAll(interceptors.Recovery(), interceptors.Logging())

	if got, want := len(s.unaryInterceptors), beforeUnary+2; got != want {
		t.Fatalf("unaryInterceptors = %d, want %d", got, want)
	}
	if got, want := len(s.streamInterceptors), beforeStream+2; got != want {
		t.Fatalf("streamInterceptors = %d, want %d", got, want)
	}
}
