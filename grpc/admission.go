package grpc

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/velocitykode/velocity/grpc/interceptors"
	"github.com/velocitykode/velocity/internal/drain"
)

// errStopping is the status of a call a stop refused: it reached the
// server after the stop began, and never ran.
var errStopping = status.Error(codes.Unavailable, "server is stopping")

// admission returns outer, the outermost interceptor pair of a server's
// chains, admitting each call into run before it: a call admitted before
// the stop began holds the run until its handler returns, so the stop's
// ServerStopped, and a nil Shutdown, come only once it has; a call that
// arrives after is refused with codes.Unavailable without running. It
// calls outer itself rather than as a separate link of grpc-go's chain,
// which allocates a continuation per link, so admission costs a call no
// allocation. It calls no application code.
func admission(run *drain.Run, outer interceptors.InterceptorPair) interceptors.InterceptorPair {
	return interceptors.InterceptorPair{
		Unary: func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			if !run.Admit() {
				return nil, errStopping
			}
			defer run.Release()
			return outer.Unary(ctx, req, info, h)
		},
		Stream: func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) error {
			if !run.Admit() {
				return errStopping
			}
			defer run.Release()
			return outer.Stream(srv, ss, info, h)
		},
	}
}
