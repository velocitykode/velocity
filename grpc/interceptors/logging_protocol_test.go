package interceptors

import (
	"context"
	"testing"

	"google.golang.org/grpc/metadata"

	"github.com/velocitykode/velocity/grpc/grpcevents"
)

// TestDetectProtocolUserAgent verifies detectProtocol classifies requests by
// user-agent without panicking on short, attacker-controlled values. Agents of
// length 1-3 previously triggered a `slice bounds out of range [:4]` panic.
func TestDetectProtocolUserAgent(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		want grpcevents.Protocol
	}{
		// Regression cases: 1-3 byte user-agents previously panicked.
		{"one byte agent", incomingUA("a"), grpcevents.ProtocolHTTP},
		{"two byte agent", incomingUA("ab"), grpcevents.ProtocolHTTP},
		{"three byte agent", incomingUA("abc"), grpcevents.ProtocolHTTP},

		// Empty string element: outer/inner guards keep it gRPC.
		{"empty agent element", incomingUA(""), grpcevents.ProtocolGRPC},

		// grpc clients stay gRPC.
		{"grpc-go versioned", incomingUA("grpc-go/1.0"), grpcevents.ProtocolGRPC},
		{"bare grpc", incomingUA("grpc"), grpcevents.ProtocolGRPC},
		{"grpcwebproxy", incomingUA("grpcwebproxy"), grpcevents.ProtocolGRPC},

		// Non-grpc browser agent => HTTP.
		{"browser agent", incomingUA("Mozilla/5.0"), grpcevents.ProtocolHTTP},

		// No metadata at all => gRPC.
		{"no metadata", context.Background(), grpcevents.ProtocolGRPC},

		// Gateway header path still works.
		{"gateway accept header", incomingMD(metadata.MD{"grpcgateway-accept": []string{"application/json"}}), grpcevents.ProtocolHTTP},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectProtocol(tt.ctx)
			if got != tt.want {
				t.Errorf("detectProtocol() = %q, want %q", got, tt.want)
			}
		})
	}
}

func incomingUA(agent string) context.Context {
	return incomingMD(metadata.MD{"user-agent": []string{agent}})
}

func incomingMD(md metadata.MD) context.Context {
	return metadata.NewIncomingContext(context.Background(), md)
}
