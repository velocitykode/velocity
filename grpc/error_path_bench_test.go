package grpc

import (
	"context"
	"errors"
	"fmt"
	"testing"

	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/velocitykode/velocity/log"
)

// BenchmarkDefaultChain_UnaryError measures one unary call whose handler
// returns an error, through the interceptors Build installs by default,
// followed by the status derivation grpc-go runs on the error the chain
// returns (status.FromError, then status.FromContextError for a
// non-status error, as grpc-go's processUnaryRPC does), so the cost of
// classifying the error is counted wherever it runs.
func BenchmarkDefaultChain_UnaryError(b *testing.B) {
	for _, ec := range []struct {
		name string
		err  error
	}{
		{"status error", status.Error(codes.NotFound, "no such user")},
		{"wrapped status error", fmt.Errorf("lookup: %w", status.Error(codes.NotFound, "no such user"))},
		{"plain error", errors.New("boom")},
	} {
		for _, dispatcher := range []bool{false, true} {
			name := ec.name + "/no dispatcher"
			if dispatcher {
				name = ec.name + "/dispatcher without listeners"
			}
			b.Run(name, func(b *testing.B) { benchErrorChain(b, ec.err, dispatcher) })
		}
	}
}

func benchErrorChain(b *testing.B, handlerErr error, dispatcher bool) {
	quiet, _ := log.NewLogger(log.LogConfig{Driver: "null"})
	s := NewServer(WithLogger(quiet))
	if dispatcher {
		s.SetEventDispatcher(func(context.Context, any) error { return nil })
	}
	chain, _ := benchChains(s, nil, nil)

	info := &grpcgo.UnaryServerInfo{FullMethod: "/svc.Bench/Do"}
	var h grpcgo.UnaryHandler = func(context.Context, any) (any, error) { return nil, handlerErr }
	for i := len(chain) - 1; i >= 0; i-- {
		ic, next := chain[i], h
		h = func(ctx context.Context, req any) (any, error) { return ic(ctx, req, info, next) }
	}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		_, err := h(ctx, nil)
		// grpc-go's processUnaryRPC.
		if st, ok := status.FromError(err); !ok {
			_ = status.FromContextError(err)
		} else {
			_ = st
		}
	}
}
