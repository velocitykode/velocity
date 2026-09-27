package interceptors_test

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/velocitykode/velocity/grpc/interceptors"
	"github.com/velocitykode/velocity/trace"
)

func outgoingOf(t *testing.T, ctx context.Context) metadata.MD {
	t.Helper()
	md, _ := metadata.FromOutgoingContext(ctx)
	return md
}

func TestPropagation_UnaryWritesCarriers(t *testing.T) {
	var seen metadata.MD
	invoker := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
		seen = outgoingOf(t, ctx)
		return nil
	}
	ctx := trace.WithRequestID(trace.WithTrace(context.Background(), callerTrace, callerSpan), "req-9")
	if err := interceptors.Propagation().Unary(ctx, "/svc/M", nil, nil, nil, invoker); err != nil {
		t.Fatal(err)
	}
	if got := seen.Get("traceparent"); len(got) != 1 || got[0] != "00-"+callerTrace+"-"+callerSpan+"-01" {
		t.Errorf("traceparent = %v", got)
	}
	if got := seen.Get("x-request-id"); len(got) != 1 || got[0] != "req-9" {
		t.Errorf("x-request-id = %v", got)
	}
}

func TestPropagation_StreamWritesCarriers(t *testing.T) {
	var seen metadata.MD
	streamer := func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		seen = outgoingOf(t, ctx)
		return nil, nil
	}
	ctx := trace.WithTrace(context.Background(), callerTrace, callerSpan)
	if _, err := interceptors.Propagation().Stream(ctx, &grpc.StreamDesc{}, nil, "/svc/S", streamer); err != nil {
		t.Fatal(err)
	}
	if got := seen.Get("traceparent"); len(got) != 1 || got[0] != "00-"+callerTrace+"-"+callerSpan+"-01" {
		t.Errorf("traceparent = %v", got)
	}
	if got := seen.Get("x-request-id"); len(got) != 0 {
		t.Errorf("x-request-id = %v, want none (ctx has no request id)", got)
	}
}

// TestPropagation_KeepsCallerMetadata pins that metadata the caller already
// set wins over the context's carriers, and that an untraced context adds
// nothing.
func TestPropagation_KeepsCallerMetadata(t *testing.T) {
	var seen metadata.MD
	invoker := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
		seen = outgoingOf(t, ctx)
		return nil
	}
	own := "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	ctx := trace.WithRequestID(trace.WithTrace(context.Background(), callerTrace, callerSpan), "from-ctx")
	ctx = metadata.AppendToOutgoingContext(ctx, "traceparent", own, "x-request-id", "from-caller")
	if err := interceptors.Propagation().Unary(ctx, "/svc/M", nil, nil, nil, invoker); err != nil {
		t.Fatal(err)
	}
	if got := seen.Get("traceparent"); len(got) != 1 || got[0] != own {
		t.Errorf("traceparent = %v, want only the caller's", got)
	}
	if got := seen.Get("x-request-id"); len(got) != 1 || got[0] != "from-caller" {
		t.Errorf("x-request-id = %v, want only the caller's", got)
	}

	seen = nil
	if err := interceptors.Propagation().Unary(context.Background(), "/svc/M", nil, nil, nil, invoker); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 0 {
		t.Errorf("untraced ctx wrote %v, want nothing", seen)
	}
}
