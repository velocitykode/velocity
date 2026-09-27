package interceptors

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/velocitykode/velocity/trace"
)

// ClientInterceptorPair holds both unary and stream client interceptor
// variants, for grpc.WithChainUnaryInterceptor and
// grpc.WithChainStreamInterceptor.
type ClientInterceptorPair struct {
	Unary  grpc.UnaryClientInterceptor
	Stream grpc.StreamClientInterceptor
}

// Propagation returns the client interceptor pair that carries the call
// context's trace and request id to the server: traceparent naming the
// context's trace and current span as the server's parent, and
// x-request-id with its request id (see trace.Propagate). The server's
// Logging interceptor continues that trace and keeps that request id. The
// HTTP gateway installs it on the client it proxies through; install it on
// any other client the application builds:
//
//	p := interceptors.Propagation()
//	conn, err := grpc.NewClient(target,
//	    grpc.WithChainUnaryInterceptor(p.Unary),
//	    grpc.WithChainStreamInterceptor(p.Stream),
//	)
//
// A key the outgoing metadata already holds is left as the caller set it.
// On a call it proxies for a gateway request, the gateway first sets both
// keys to the carriers it selected, so the gRPC server sees the request id
// the gateway echoes.
func Propagation() ClientInterceptorPair {
	return ClientInterceptorPair{
		Unary: func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			return invoker(propagate(ctx), method, req, reply, cc, opts...)
		},
		Stream: func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
			return streamer(propagate(ctx), desc, cc, method, opts...)
		},
	}
}

// propagate returns ctx with the trace carriers appended to its outgoing
// metadata, skipping any key the metadata already holds.
func propagate(ctx context.Context) context.Context {
	existing, _ := metadata.FromOutgoingContext(ctx)
	var kv []string
	trace.Propagate(ctx, func(name, value string) {
		key := strings.ToLower(name)
		if len(existing.Get(key)) > 0 {
			return
		}
		kv = append(kv, key, value)
	})
	if len(kv) == 0 {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, kv...)
}
