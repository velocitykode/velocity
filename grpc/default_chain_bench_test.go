package grpc

import (
	"context"
	"testing"

	grpcgo "google.golang.org/grpc"

	"github.com/velocitykode/velocity/grpc/interceptors"
	"github.com/velocitykode/velocity/log"
)

// BenchmarkDefaultChain_Unary measures one successful unary call through
// the interceptors Build installs by default (no user interceptors, no
// reporter), the chain composed once as grpc-go's chained interceptor
// composes it: without an event dispatcher, and with an app dispatcher
// that has no listener for the gRPC events.
func BenchmarkDefaultChain_Unary(b *testing.B) {
	b.Run("no dispatcher", func(b *testing.B) { benchDefaultChain(b, false) })
	b.Run("dispatcher without listeners", func(b *testing.B) { benchDefaultChain(b, true) })
}

func benchDefaultChain(b *testing.B, dispatcher bool) {
	quiet, _ := log.NewLogger(log.LogConfig{Driver: "null"})
	s := NewServer(WithLogger(quiet))
	if dispatcher {
		// A dispatcher with nothing listening: the events are built and
		// handed over, and dropped.
		s.SetEventDispatcher(func(context.Context, any) error { return nil })
	}
	calls := interceptors.CallLifecycle(
		interceptors.WithLogger(s.logger),
		interceptors.WithEventDispatcher(s.eventDispatchFunc()),
		interceptors.WithReporter(s.reporter),
	)
	chain := []grpcgo.UnaryServerInterceptor{calls.Unary, calls.Unary}

	info := &grpcgo.UnaryServerInfo{FullMethod: "/svc.Bench/Do"}
	var h grpcgo.UnaryHandler = func(context.Context, any) (any, error) { return nil, nil }
	for i := len(chain) - 1; i >= 0; i-- {
		ic, next := chain[i], h
		h = func(ctx context.Context, req any) (any, error) { return ic(ctx, req, info, next) }
	}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_, _ = h(ctx, nil)
	}
}
