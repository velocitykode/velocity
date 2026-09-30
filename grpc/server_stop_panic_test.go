package grpc_test

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	grpcgo "google.golang.org/grpc"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc"
	"github.com/velocitykode/velocity/internal/testnet"
)

// stopPanicLogger panics on every stop diagnostic and counts the other
// lines.
type stopPanicLogger struct{ lines atomic.Int32 }

func (l *stopPanicLogger) line(msg string) {
	if strings.Contains(msg, "stop") || strings.Contains(msg, "shutting down") {
		panic("logger broke: " + msg)
	}
	l.lines.Add(1)
}
func (l *stopPanicLogger) Debug(msg string, _ ...any)  { l.line(msg) }
func (l *stopPanicLogger) Info(msg string, _ ...any)   { l.line(msg) }
func (l *stopPanicLogger) Warn(msg string, _ ...any)   { l.line(msg) }
func (l *stopPanicLogger) Error(msg string, _ ...any)  { l.line(msg) }
func (l *stopPanicLogger) Fatal(msg string, _ ...any)  { l.line(msg) }
func (l *stopPanicLogger) With(...any) contract.Logger { return l }

// closeTracker records that its listener was closed.
type closeTracker struct {
	net.Listener
	closed atomic.Bool
}

func (l *closeTracker) Close() error {
	l.closed.Store(true)
	return l.Listener.Close()
}

// noPanic runs fn and reports a panic that escapes it.
func noPanic(t *testing.T, what string, fn func()) {
	t.Helper()
	defer func() {
		if p := recover(); p != nil {
			t.Errorf("%s panicked: %v", what, p)
		}
	}()
	fn()
}

// A logger that panics on the stop line skips neither the transport stop
// nor ServerStopped: the listener is closed, the event is dispatched once,
// and a retry is harmless.
func TestServerStop_PanickingLoggerStillStops(t *testing.T) {
	for name, stop := range map[string]func(*grpc.Server){
		"Stop":         (*grpc.Server).Stop,
		"GracefulStop": (*grpc.Server).GracefulStop,
		"Shutdown":     func(s *grpc.Server) { _ = s.Shutdown(context.Background()) },
	} {
		t.Run(name, func(t *testing.T) {
			lis := &closeTracker{Listener: testnet.Loopback(t)}
			s := grpc.NewServer(grpc.WithListener(lis), grpc.WithLogger(&stopPanicLogger{}))
			counter := &stoppedCounter{}
			s.SetEventDispatcher(counter.dispatch)
			startHealth(t, s)

			noPanic(t, name, func() { stop(s) })
			// GracefulStop starts the drain and returns: wait for it.
			waitFor(t, "the listener to close", lis.closed.Load)
			if s.IsRunning() {
				t.Error("server still running after the stop")
			}
			noPanic(t, "retry", func() {
				s.Stop()
				s.GracefulStop()
				_ = s.Shutdown(context.Background())
			})
			waitFor(t, "ServerStopped", func() bool { return counter.n.Load() >= 1 })
			if got := counter.n.Load(); got != 1 {
				t.Errorf("ServerStopped dispatched %d times, want 1", got)
			}
		})
	}
}

// A gateway logger that panics on the stop line does not leave the HTTP
// server serving: its listener is closed, and a retry is harmless.
func TestGatewayStop_PanickingLoggerStillStops(t *testing.T) {
	for name, stop := range map[string]func(*grpc.Gateway){
		"Stop":     (*grpc.Gateway).Stop,
		"Shutdown": func(g *grpc.Gateway) { _ = g.Shutdown(context.Background()) },
	} {
		t.Run(name, func(t *testing.T) {
			lis := &closeTracker{Listener: testnet.Loopback(t)}
			g := grpc.NewGateway(grpc.GatewayWithListener(lis), grpc.GatewayWithGRPCEndpoint("127.0.0.1:1"),
				grpc.GatewayWithEnvironment("development"), grpc.GatewayWithLogger(&stopPanicLogger{}))
			g.RegisterHandler(func(context.Context, *runtime.ServeMux, string, []grpcgo.DialOption) error { return nil })
			if err := g.StartAsync(); err != nil {
				t.Fatalf("StartAsync: %v", err)
			}
			addr := lis.Addr().String()
			up := false
			for range 200 {
				if c, err := net.Dial("tcp", addr); err == nil {
					_ = c.Close()
					up = true
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if !up {
				t.Fatal("gateway never listened")
			}
			t.Cleanup(func() {
				// A gateway a broken stop left serving must not leak.
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				g.Stop()
				_ = g.Shutdown(ctx)
			})

			noPanic(t, name, func() { stop(g) })
			if !lis.closed.Load() {
				t.Error("the gateway's listener is still open after the stop")
			}
			if c, err := net.Dial("tcp", addr); err == nil {
				_ = c.Close()
				t.Error("gateway still accepting connections after the stop")
			}
			noPanic(t, "retry", func() {
				g.Stop()
				_ = g.Shutdown(context.Background())
			})
			if g.IsRunning() {
				t.Error("gateway still running")
			}
			if _, err := http.Get("http://" + addr + "/"); err == nil {
				t.Error("gateway still serving HTTP after the stop")
			}
		})
	}
}
