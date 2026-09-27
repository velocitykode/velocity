package grpc_test

import (
	"context"
	"testing"

	"google.golang.org/grpc/metadata"

	"github.com/velocitykode/velocity/grpc"
)

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
