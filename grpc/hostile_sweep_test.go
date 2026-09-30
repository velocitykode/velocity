package grpc_test

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc"
	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/internal/testnet"
)

// armedLogger passes each line to a hostile logger while armed and drops
// it otherwise, so a sweep can set a component up with quiet user code and
// turn it hostile for one entry point.
type armedLogger struct {
	armed *atomic.Bool
	h     *hostile.Logger
}

func (l armedLogger) Debug(msg string, kvs ...any) {
	if l.armed.Load() {
		l.h.Debug(msg, kvs...)
	}
}

func (l armedLogger) Info(msg string, kvs ...any) {
	if l.armed.Load() {
		l.h.Info(msg, kvs...)
	}
}

func (l armedLogger) Warn(msg string, kvs ...any) {
	if l.armed.Load() {
		l.h.Warn(msg, kvs...)
	}
}

func (l armedLogger) Error(msg string, kvs ...any) {
	if l.armed.Load() {
		l.h.Error(msg, kvs...)
	}
}

func (l armedLogger) Fatal(msg string, kvs ...any) {
	if l.armed.Load() {
		l.h.Fatal(msg, kvs...)
	}
}

func (l armedLogger) With(kvs ...any) contract.Logger {
	if l.armed.Load() {
		l.h.With(kvs...)
	}
	return l
}

// hostileSite is where a sweep puts the hostile code: the component's
// logger or its event dispatcher.
type hostileSite string

const (
	siteLogger     hostileSite = "logger"
	siteDispatcher hostileSite = "dispatcher"
)

// sweep runs body once per mode and site. A panic mode case runs in a
// child process, so a panic that escapes containment fails that case, not
// the package run.
func sweep(t *testing.T, body func(t *testing.T, mode hostile.Mode, site hostileSite)) {
	for _, mode := range hostile.Modes() {
		for _, site := range []hostileSite{siteLogger, siteDispatcher} {
			t.Run(mode.String()+"/"+string(site), func(t *testing.T) {
				if mode == hostile.Panic {
					hostile.Isolated(t, func() { body(t, mode, site) })
					return
				}
				body(t, mode, site)
			})
		}
	}
}

// boundedShutdown checks that Shutdown with a short deadline returns about
// on time whatever the user code is doing.
func boundedShutdown(t *testing.T, shutdown func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if p := hostile.Within(t, 2*time.Second, func() { _ = shutdown(ctx) }); p != nil {
		t.Errorf("Shutdown panicked: %v", p)
	}
}

// entered waits until the code runs or done closes, whichever is first.
func entered(c *hostile.Code, done <-chan struct{}) {
	select {
	case <-c.Entered():
	case <-done:
	case <-time.After(time.Second):
	}
}

// servesOn reports whether a start entry point should have left the
// component serving: a Block case ran a bounded Shutdown, and a re-entry
// shuts it down. It waits for a re-entry to finish, failing the test when
// the re-entry does not return.
func servesOn(t *testing.T, mode hostile.Mode, code *hostile.Code, reentered <-chan struct{}) bool {
	t.Helper()
	switch mode {
	case hostile.Block:
		return false
	case hostile.Reenter:
		select {
		case <-code.Entered():
		case <-time.After(200 * time.Millisecond):
			return true
		}
		select {
		case <-reentered:
		case <-time.After(hostile.Deadline):
			t.Fatal("the re-entry into the component did not return")
		}
		return false
	}
	return true
}

// serverEntry is one Server entry point a sweep turns the user code
// hostile for. setup brings the server to the state the entry point
// starts from; call runs it and reports its error.
type serverEntry struct {
	name  string
	setup func(t *testing.T, s *grpc.Server)
	call  func(s *grpc.Server) error
	// stops is set for an entry point that ends the server.
	stops bool
}

func serving(t *testing.T, s *grpc.Server) {
	t.Helper()
	if err := s.StartAsync(); err != nil {
		t.Fatalf("StartAsync: %v", err)
	}
	healthOK(t, s)
}

// healthOK fails the test unless a health check on s succeeds.
func healthOK(t *testing.T, s *grpc.Server) {
	t.Helper()
	conn, err := grpcgo.NewClient(s.Address(), grpcgo.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := grpc_health_v1.NewHealthClient(conn).Check(ctx, &grpc_health_v1.HealthCheckRequest{}); err != nil {
		t.Errorf("health check: %v", err)
	}
}

var serverEntries = []serverEntry{
	{name: "Build", call: (*grpc.Server).Build},
	{name: "StartAsync", call: (*grpc.Server).StartAsync},
	{name: "Start", call: func(s *grpc.Server) error {
		errc := make(chan error, 1)
		go func() { errc <- s.Start() }()
		select {
		case err := <-errc:
			return err
		case <-time.After(50 * time.Millisecond):
			return nil // serving, or held by its own user code
		}
	}},
	{name: "Stop", setup: serving, stops: true, call: func(s *grpc.Server) error { s.Stop(); return nil }},
	{name: "GracefulStop", setup: serving, stops: true, call: func(s *grpc.Server) error { s.GracefulStop(); return nil }},
	{name: "Shutdown", setup: serving, stops: true, call: func(s *grpc.Server) error {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		return s.Shutdown(ctx)
	}},
}

// Every Server entry point survives a logger or event dispatcher that
// panics, blocks or calls back into the server: no panic escapes, a
// bounded Shutdown returns on time while the code blocks, the entry point
// returns once the code does, and the server then serves or stops as it
// should.
func TestServer_HostileUserCodeSweep(t *testing.T) {
	for _, e := range serverEntries {
		t.Run(e.name, func(t *testing.T) {
			sweep(t, func(t *testing.T, mode hostile.Mode, site hostileSite) {
				var armed atomic.Bool
				var s *grpc.Server
				reentered := make(chan struct{})
				code := hostile.New(t, mode, func() {
					defer close(reentered)
					_ = s.IsRunning()
					_ = s.Address()
					ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
					defer cancel()
					_ = s.Shutdown(ctx)
				})
				opts := []grpc.ServerOption{grpc.WithListener(testnet.Loopback(t))}
				if site == siteLogger {
					opts = append(opts, grpc.WithLogger(armedLogger{armed: &armed, h: hostile.NewLogger(code)}))
				}
				s = grpc.NewServer(opts...)
				if site == siteDispatcher {
					d := hostile.NewDispatcher(code)
					s.SetEventDispatcher(func(ctx context.Context, ev any) error {
						if armed.Load() {
							return d.Dispatch(ctx, ev)
						}
						return nil
					})
				}
				s.RegisterService(func(srv any) {
					grpc_health_v1.RegisterHealthServer(srv.(*grpcgo.Server), health.NewServer())
				})
				stopOnCleanup(t, s)
				if e.setup != nil {
					e.setup(t, s)
				}

				armed.Store(true)
				done := make(chan struct{})
				var callErr error
				var panicked any
				go func() {
					defer close(done)
					panicked = hostile.Within(t, 10*time.Second, func() { callErr = e.call(s) })
				}()
				if mode == hostile.Block {
					entered(code, done)
					boundedShutdown(t, s.Shutdown)
					code.Release()
				}
				select {
				case <-done:
				case <-time.After(hostile.Deadline):
					t.Fatalf("%s did not return within %v", e.name, hostile.Deadline)
				}
				code.Disarm()
				armed.Store(false)
				if panicked != nil {
					t.Fatalf("%s panicked: %v", e.name, panicked)
				}

				// A reentered or blocked case may have stopped the server;
				// that is the only error an entry point may report.
				if callErr != nil && !errors.Is(callErr, grpcgo.ErrServerStopped) {
					t.Errorf("%s = %v", e.name, callErr)
				}
				if !e.stops && callErr == nil && servesOn(t, mode, code, reentered) {
					if err := s.StartAsync(); err != nil && !errors.Is(err, grpc.ErrServerAlreadyRunning) &&
						!errors.Is(err, grpcgo.ErrServerStopped) {
						t.Errorf("StartAsync after %s = %v", e.name, err)
					}
					if s.IsRunning() {
						healthOK(t, s)
					}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				var err error
				if p := hostile.Within(t, 3*time.Second, func() { err = s.Shutdown(ctx) }); p != nil {
					t.Fatalf("the closing Shutdown panicked: %v", p)
				}
				if err != nil && !errors.Is(err, grpcgo.ErrServerStopped) {
					t.Errorf("the closing Shutdown = %v", err)
				}
				if s.IsRunning() {
					t.Error("the server still runs after Shutdown")
				}
			})
		})
	}
}

// gatewayEntry is one Gateway entry point a sweep turns the logger
// hostile for.
type gatewayEntry struct {
	name  string
	setup func(t *testing.T, g *grpc.Gateway, addr string)
	call  func(g *grpc.Gateway) error
	stops bool
}

func gatewayServing(t *testing.T, g *grpc.Gateway, addr string) {
	t.Helper()
	if err := g.StartAsync(); err != nil {
		t.Fatalf("StartAsync: %v", err)
	}
	gatewayOK(t, addr)
}

// gatewayOK fails the test unless the gateway at addr answers.
func gatewayOK(t *testing.T, addr string) {
	t.Helper()
	var err error
	for range 200 {
		var resp *http.Response
		if resp, err = http.Get("http://" + addr + "/"); err == nil {
			_ = resp.Body.Close()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("the gateway does not answer: %v", err)
}

var gatewayEntries = []gatewayEntry{
	{name: "Build", call: func(g *grpc.Gateway) error { return g.Build(context.Background()) }},
	{name: "StartAsync", call: (*grpc.Gateway).StartAsync},
	{name: "Start", call: func(g *grpc.Gateway) error {
		errc := make(chan error, 1)
		go func() { errc <- g.Start() }()
		select {
		case err := <-errc:
			return err
		case <-time.After(50 * time.Millisecond):
			return nil
		}
	}},
	{name: "Stop", setup: gatewayServing, stops: true, call: func(g *grpc.Gateway) error { g.Stop(); return nil }},
	{name: "Shutdown", setup: gatewayServing, stops: true, call: func(g *grpc.Gateway) error {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		return g.Shutdown(ctx)
	}},
}

// Every Gateway entry point survives a logger that panics, blocks or
// calls back into the gateway (the gateway has no event dispatcher).
func TestGateway_HostileUserCodeSweep(t *testing.T) {
	for _, e := range gatewayEntries {
		t.Run(e.name, func(t *testing.T) {
			for _, mode := range hostile.Modes() {
				t.Run(mode.String(), func(t *testing.T) {
					body := func() { gatewaySweepCase(t, e, mode) }
					if mode == hostile.Panic {
						hostile.Isolated(t, body)
						return
					}
					body()
				})
			}
		})
	}
}

func gatewaySweepCase(t *testing.T, e gatewayEntry, mode hostile.Mode) {
	var armed atomic.Bool
	var g *grpc.Gateway
	reentered := make(chan struct{})
	code := hostile.New(t, mode, func() {
		defer close(reentered)
		_ = g.IsRunning()
		_ = g.Address()
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_ = g.Shutdown(ctx)
	})
	lis := testnet.Loopback(t)
	addr := lis.Addr().String()
	g = grpc.NewGateway(grpc.GatewayWithListener(lis), grpc.GatewayWithGRPCEndpoint("127.0.0.1:1"),
		grpc.GatewayWithEnvironment("development"),
		grpc.GatewayWithLogger(armedLogger{armed: &armed, h: hostile.NewLogger(code)}))
	g.RegisterHandler(func(context.Context, *runtime.ServeMux, string, []grpcgo.DialOption) error { return nil })
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = g.Shutdown(ctx)
	})
	if e.setup != nil {
		e.setup(t, g, addr)
	}

	armed.Store(true)
	done := make(chan struct{})
	var callErr error
	var panicked any
	go func() {
		defer close(done)
		panicked = hostile.Within(t, 10*time.Second, func() { callErr = e.call(g) })
	}()
	if mode == hostile.Block {
		entered(code, done)
		boundedShutdown(t, g.Shutdown)
		code.Release()
	}
	select {
	case <-done:
	case <-time.After(hostile.Deadline):
		t.Fatalf("%s did not return within %v", e.name, hostile.Deadline)
	}
	code.Disarm()
	armed.Store(false)
	if panicked != nil {
		t.Fatalf("%s panicked: %v", e.name, panicked)
	}
	if callErr != nil && !errors.Is(callErr, http.ErrServerClosed) {
		t.Errorf("%s = %v", e.name, callErr)
	}
	if !e.stops && callErr == nil && servesOn(t, mode, code, reentered) {
		if err := g.StartAsync(); err != nil && !errors.Is(err, grpc.ErrServerAlreadyRunning) &&
			!errors.Is(err, http.ErrServerClosed) {
			t.Errorf("StartAsync after %s = %v", e.name, err)
		}
		if g.IsRunning() {
			gatewayOK(t, addr)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var err error
	if p := hostile.Within(t, 3*time.Second, func() { err = g.Shutdown(ctx) }); p != nil {
		t.Fatalf("the closing Shutdown panicked: %v", p)
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Errorf("the closing Shutdown = %v", err)
	}
	if g.IsRunning() {
		t.Error("the gateway still runs after Shutdown")
	}
}
