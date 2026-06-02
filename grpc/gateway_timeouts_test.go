package grpc

import (
	"context"
	"testing"
	"time"
)

// buildGateway constructs a gateway far enough to populate httpServer. Insecure
// dial credentials and a valid endpoint are supplied so Build() reaches the
// http.Server construction without registrations.
func buildGateway(t *testing.T, opts ...GatewayOption) *Gateway {
	t.Helper()
	base := []GatewayOption{
		GatewayWithGRPCEndpoint("localhost:50051"),
		GatewayWithInsecure(),
	}
	g := NewGateway(append(base, opts...)...)
	if err := g.Build(context.Background()); err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if g.httpServer == nil {
		t.Fatal("Build() did not populate httpServer")
	}
	return g
}

func TestGatewayDefaultServerTimeouts(t *testing.T) {
	g := buildGateway(t)
	srv := g.httpServer

	if got, want := srv.ReadHeaderTimeout, 10*time.Second; got != want {
		t.Errorf("ReadHeaderTimeout = %v, want %v", got, want)
	}
	if got, want := srv.ReadTimeout, defaultGatewayReadTimeout; got != want {
		t.Errorf("ReadTimeout = %v, want %v", got, want)
	}
	if got, want := srv.WriteTimeout, defaultGatewayWriteTimeout; got != want {
		t.Errorf("WriteTimeout = %v, want %v", got, want)
	}
	if got, want := srv.IdleTimeout, defaultGatewayIdleTimeout; got != want {
		t.Errorf("IdleTimeout = %v, want %v", got, want)
	}
	if got, want := srv.MaxHeaderBytes, defaultGatewayMaxHeaderBytes; got != want {
		t.Errorf("MaxHeaderBytes = %v, want %v", got, want)
	}

	// Secure-by-default: a zero-option gateway must get non-zero bounds.
	if srv.ReadTimeout == 0 || srv.WriteTimeout == 0 || srv.IdleTimeout == 0 || srv.MaxHeaderBytes == 0 {
		t.Errorf("zero-option gateway has a zero server bound: read=%v write=%v idle=%v maxHeader=%v",
			srv.ReadTimeout, srv.WriteTimeout, srv.IdleTimeout, srv.MaxHeaderBytes)
	}
}

func TestGatewayServerTimeoutOptions(t *testing.T) {
	tests := []struct {
		name   string
		option GatewayOption
		check  func(t *testing.T, g *Gateway)
	}{
		{
			name:   "ReadTimeout",
			option: GatewayWithReadTimeout(5 * time.Second),
			check: func(t *testing.T, g *Gateway) {
				if got, want := g.httpServer.ReadTimeout, 5*time.Second; got != want {
					t.Errorf("ReadTimeout = %v, want %v", got, want)
				}
			},
		},
		{
			name:   "WriteTimeout",
			option: GatewayWithWriteTimeout(7 * time.Second),
			check: func(t *testing.T, g *Gateway) {
				if got, want := g.httpServer.WriteTimeout, 7*time.Second; got != want {
					t.Errorf("WriteTimeout = %v, want %v", got, want)
				}
			},
		},
		{
			name:   "IdleTimeout",
			option: GatewayWithIdleTimeout(11 * time.Second),
			check: func(t *testing.T, g *Gateway) {
				if got, want := g.httpServer.IdleTimeout, 11*time.Second; got != want {
					t.Errorf("IdleTimeout = %v, want %v", got, want)
				}
			},
		},
		{
			name:   "MaxHeaderBytes",
			option: GatewayWithMaxHeaderBytes(4096),
			check: func(t *testing.T, g *Gateway) {
				if got, want := g.httpServer.MaxHeaderBytes, 4096; got != want {
					t.Errorf("MaxHeaderBytes = %v, want %v", got, want)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := buildGateway(t, tt.option)
			tt.check(t, g)
		})
	}
}
