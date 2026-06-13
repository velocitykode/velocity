package grpc_test

import (
	"context"
	"testing"

	"google.golang.org/grpc/metadata"

	"github.com/velocitykode/velocity/grpc"
	"github.com/velocitykode/velocity/grpc/interceptors"
)

// TestRequestIDCrossPackage verifies the grpc and interceptors packages share
// a single request-ID context key: a value set through one package is visible
// through the other in both directions.
func TestRequestIDCrossPackage(t *testing.T) {
	t.Run("set via grpc, read via interceptors", func(t *testing.T) {
		ctx := grpc.ContextWithRequestID(context.Background(), "abc-123")
		if got := interceptors.RequestIDFromContext(ctx); got != "abc-123" {
			t.Fatalf("interceptors.RequestIDFromContext = %q, want %q", got, "abc-123")
		}
	})

	t.Run("set via interceptors, read via grpc", func(t *testing.T) {
		ctx := interceptors.ContextWithRequestID(context.Background(), "xyz-789")
		if got := grpc.RequestIDFromContext(ctx); got != "xyz-789" {
			t.Fatalf("grpc.RequestIDFromContext = %q, want %q", got, "xyz-789")
		}
	})
}

// TestExtractBearerToken pins the bearer-token extraction behavior after the
// dedupe: it now delegates to interceptors.BearerTokenFromContext and flattens
// any error to the empty string.
func TestExtractBearerToken(t *testing.T) {
	cases := []struct {
		name   string
		header string // authorization header value; "" means no header set
		setMD  bool
		want   string
	}{
		{name: "no metadata", setMD: false, want: ""},
		{name: "empty token after prefix", header: "Bearer ", setMD: true, want: ""},
		{name: "lowercase prefix rejected", header: "bearer x", setMD: true, want: ""},
		{name: "valid token", header: "Bearer x", setMD: true, want: "x"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.setMD {
				ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", tc.header))
			}
			if got := grpc.ExtractBearerToken(ctx); got != tc.want {
				t.Fatalf("ExtractBearerToken = %q, want %q", got, tc.want)
			}
		})
	}
}
