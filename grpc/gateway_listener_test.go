package grpc_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	grpcgo "google.golang.org/grpc"

	"github.com/velocitykode/velocity/grpc"
	"github.com/velocitykode/velocity/internal/testnet"
)

// A gateway given a listener serves on it, reports its address, and
// closes it when it stops.
func TestGatewayWithListener_ServesOnTheCallersListener(t *testing.T) {
	lis := &closeTracker{Listener: testnet.Loopback(t)}
	g := grpc.NewGateway(grpc.GatewayWithListener(lis), grpc.GatewayWithPort("1"),
		grpc.GatewayWithGRPCEndpoint("127.0.0.1:1"), grpc.GatewayWithEnvironment("development"))
	g.RegisterHandler(func(context.Context, *runtime.ServeMux, string, []grpcgo.DialOption) error { return nil })
	g.Use(func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	})
	if err := g.StartAsync(); err != nil {
		t.Fatalf("StartAsync: %v", err)
	}
	if got, want := g.Address(), lis.Addr().String(); got != want {
		t.Errorf("Address = %q, want the listener's %q", got, want)
	}
	resp, err := http.Get("http://" + lis.Addr().String() + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want %d from the gateway's handler", resp.StatusCode, http.StatusTeapot)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := g.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if !lis.closed.Load() {
		t.Error("the listener is still open after Shutdown")
	}
}

// A gateway stopped before any Start never took the listener: it is left
// open for the caller.
func TestGatewayWithListener_StopBeforeStartLeavesItOpen(t *testing.T) {
	lis := &closeTracker{Listener: testnet.Loopback(t)}
	g := grpc.NewGateway(grpc.GatewayWithListener(lis),
		grpc.GatewayWithGRPCEndpoint("127.0.0.1:1"), grpc.GatewayWithEnvironment("development"))
	g.RegisterHandler(func(context.Context, *runtime.ServeMux, string, []grpcgo.DialOption) error { return nil })
	if err := g.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	g.Stop()
	if lis.closed.Load() {
		t.Fatal("a stop before any Start closed the caller's listener")
	}
	tcp := lis.Listener.(*net.TCPListener)
	_ = tcp.SetDeadline(time.Now().Add(20 * time.Millisecond))
	var ne net.Error
	if _, err := tcp.Accept(); !errors.As(err, &ne) || !ne.Timeout() {
		t.Errorf("Accept = %v, want a timeout from an open listener", err)
	}
}

// A start that fails (here the production guard refusing cleartext) never
// took the listener: it is left open for the caller.
func TestGatewayWithListener_FailedStartLeavesItOpen(t *testing.T) {
	lis := &closeTracker{Listener: testnet.Loopback(t)}
	g := grpc.NewGateway(grpc.GatewayWithListener(lis),
		grpc.GatewayWithGRPCEndpoint("127.0.0.1:1"), grpc.GatewayWithEnvironment("production"))
	g.RegisterHandler(func(context.Context, *runtime.ServeMux, string, []grpcgo.DialOption) error { return nil })
	if err := g.StartAsync(); err == nil {
		g.Stop()
		t.Fatal("StartAsync in production without transport credentials succeeded, want the guard's error")
	}
	if lis.closed.Load() {
		t.Error("a failed start closed the caller's listener")
	}
}
