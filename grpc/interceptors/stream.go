package interceptors

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// tracedServerStream forwards every ServerStream method to the wrapped
// stream and overrides Context() so handlers (and any nested grpc layer
// that walks back to the outermost stream) see the effective ctx the call
// lifecycle interceptor gave the call. Methods are listed explicitly
// rather than embedded so a future ServerStream addition is a
// compile-time miss rather than a silent fallthrough that bypasses the
// minted ctx.
type tracedServerStream struct {
	inner grpc.ServerStream
	ctx   context.Context
}

func (s *tracedServerStream) Context() context.Context        { return s.ctx }
func (s *tracedServerStream) SetHeader(md metadata.MD) error  { return s.inner.SetHeader(md) }
func (s *tracedServerStream) SendHeader(md metadata.MD) error { return s.inner.SendHeader(md) }
func (s *tracedServerStream) SetTrailer(md metadata.MD)       { s.inner.SetTrailer(md) }
func (s *tracedServerStream) SendMsg(m interface{}) error     { return s.inner.SendMsg(m) }
func (s *tracedServerStream) RecvMsg(m interface{}) error     { return s.inner.RecvMsg(m) }
