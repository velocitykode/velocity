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
// event dispatcher, no reporter), the chain composed once as grpc-go's
// chained interceptor composes it.
func BenchmarkDefaultChain_Unary(b *testing.B) {
	quiet, _ := log.NewLogger(log.LogConfig{Driver: "null"})
	s := NewServer(WithLogger(quiet))
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
