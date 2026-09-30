package grpc

import (
	"context"
	"testing"

	grpcgo "google.golang.org/grpc"

	"github.com/velocitykode/velocity/internal/drain"
	"github.com/velocitykode/velocity/log"
)

// BenchmarkDefaultChain_Unary measures one successful unary call through
// the interceptors Build installs by default (admission into the server's
// run and the call lifecycle; no user interceptors, no reporter), the
// chain composed once as grpc-go's chained interceptor composes it:
// without an event dispatcher, and with an app dispatcher that has no
// listener for the gRPC events.
//
// The "2 user interceptors" variants add two pass-through interceptors,
// each wrapped in interceptors.ContainUnary as Build wraps them, and the
// chain is composed as grpc-go composes it (one continuation per hop).
func BenchmarkDefaultChain_Unary(b *testing.B) {
	b.Run("no dispatcher", func(b *testing.B) { benchDefaultChain(b, false) })
	b.Run("dispatcher without listeners", func(b *testing.B) { benchDefaultChain(b, true) })
	b.Run("no dispatcher/2 user interceptors", func(b *testing.B) { benchUserChain(b, false) })
	b.Run("dispatcher without listeners/2 user interceptors", func(b *testing.B) { benchUserChain(b, true) })
}

func benchUserChain(b *testing.B, dispatcher bool) {
	quiet, _ := log.NewLogger(log.LogConfig{Driver: "null"})
	s := NewServer(WithLogger(quiet))
	if dispatcher {
		s.SetEventDispatcher(func(context.Context, any) error { return nil })
	}
	pass := func(ctx context.Context, req any, _ *grpcgo.UnaryServerInfo, h grpcgo.UnaryHandler) (any, error) {
		return h(ctx, req)
	}
	chain, _ := benchChains(s, []grpcgo.UnaryServerInterceptor{pass, pass}, nil)
	info := &grpcgo.UnaryServerInfo{FullMethod: "/svc.Bench/Do"}
	final := func(context.Context, any) (any, error) { return nil, nil }
	// grpc-go's chainUnaryInterceptors and getChainUnaryHandler.
	var next func(curr int) grpcgo.UnaryHandler
	next = func(curr int) grpcgo.UnaryHandler {
		if curr == len(chain)-1 {
			return final
		}
		return func(ctx context.Context, req any) (any, error) {
			return chain[curr+1](ctx, req, info, next(curr+1))
		}
	}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_, _ = chain[0](ctx, nil, info, next(0))
	}
}

func benchDefaultChain(b *testing.B, dispatcher bool) {
	quiet, _ := log.NewLogger(log.LogConfig{Driver: "null"})
	s := NewServer(WithLogger(quiet))
	if dispatcher {
		// A dispatcher with nothing listening: the events are built and
		// handed over, and dropped.
		s.SetEventDispatcher(func(context.Context, any) error { return nil })
	}
	chain, _ := benchChains(s, nil, nil)

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

// benchChains returns the chains Build installs on s, with unary and
// stream as the user interceptors, admitting into a run of its own.
func benchChains(s *Server, unary []grpcgo.UnaryServerInterceptor, stream []grpcgo.StreamServerInterceptor) ([]grpcgo.UnaryServerInterceptor, []grpcgo.StreamServerInterceptor) {
	var own drain.Owner
	return interceptorChains(own.NewRun(), s.defaultCallLifecycle(s.logger, s.reporter, nil), unary, stream)
}

// benchStream is a server stream that only carries a context.
type benchStream struct {
	grpcgo.ServerStream
	ctx context.Context
}

func (s benchStream) Context() context.Context { return s.ctx }

// BenchmarkDefaultChain_Stream measures one stream call through the
// default chain with two pass-through user interceptors, each wrapped in
// interceptors.ContainStream as Build wraps them, composed as grpc-go
// composes a stream chain, without an event dispatcher.
func BenchmarkDefaultChain_Stream(b *testing.B) {
	quiet, _ := log.NewLogger(log.LogConfig{Driver: "null"})
	s := NewServer(WithLogger(quiet))
	pass := func(srv any, ss grpcgo.ServerStream, _ *grpcgo.StreamServerInfo, h grpcgo.StreamHandler) error {
		return h(srv, ss)
	}
	_, chain := benchChains(s, nil, []grpcgo.StreamServerInterceptor{pass, pass})
	info := &grpcgo.StreamServerInfo{FullMethod: "/svc.Bench/Stream"}
	final := func(any, grpcgo.ServerStream) error { return nil }
	var next func(curr int) grpcgo.StreamHandler
	next = func(curr int) grpcgo.StreamHandler {
		if curr == len(chain)-1 {
			return final
		}
		return func(srv any, ss grpcgo.ServerStream) error {
			return chain[curr+1](srv, ss, info, next(curr+1))
		}
	}
	ss := benchStream{ctx: context.Background()}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = chain[0](nil, ss, info, next(0))
	}
}
