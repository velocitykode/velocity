package grpc_test

import (
	"context"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc"
	"github.com/velocitykode/velocity/grpc/grpcevents"
	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/internal/testnet"
)

// gateListener's Close signals entered and blocks until release.
type gateListener struct {
	net.Listener
	once             sync.Once
	entered, release chan struct{}
	closed           atomic.Bool
}

func newGateListener(t *testing.T) *gateListener {
	return &gateListener{Listener: testnet.Loopback(t), entered: make(chan struct{}), release: make(chan struct{})}
}

func (l *gateListener) Close() error {
	l.once.Do(func() {
		close(l.entered)
		<-l.release
	})
	l.closed.Store(true)
	return l.Listener.Close()
}

// A stop of a server built but never served closes its listener inside
// the stop's drain: a Shutdown that overlaps it, while the listener's
// Close still runs, does not report the server stopped before that Close
// has returned.
func TestServerShutdown_WaitsForAnUnservedListenersClose(t *testing.T) {
	for name, first := range stopCalls {
		t.Run(name, func(t *testing.T) {
			lis := newGateListener(t)
			s := grpc.NewServer(grpc.WithListener(lis), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
			if err := s.Build(); err != nil {
				t.Fatalf("Build: %v", err)
			}
			firstDone := make(chan struct{})
			go func() {
				defer close(firstDone)
				_ = first(s)
			}()
			select {
			case <-lis.entered:
			case <-time.After(hostile.Deadline):
				t.Fatalf("%s did not close the listener", name)
			}
			second := make(chan error, 1)
			go func() { second <- s.Shutdown(context.Background()) }()
			select {
			case err := <-second:
				t.Fatalf("Shutdown returned %v while the listener's Close still ran", err)
			case <-time.After(100 * time.Millisecond):
			}
			close(lis.release)
			select {
			case err := <-second:
				if err != nil {
					t.Errorf("Shutdown = %v, want nil", err)
				}
				if !lis.closed.Load() {
					t.Error("Shutdown returned before the listener's Close did")
				}
			case <-time.After(hostile.Deadline):
				t.Fatal("Shutdown did not return after the Close")
			}
			select {
			case <-firstDone:
			case <-time.After(hostile.Deadline):
				t.Fatalf("%s did not return", name)
			}
		})
	}
}

// The close of a never served server's listener is the stop's own work:
// a Shutdown called from that Close is refused at once instead of
// waiting on the stop running it, and a Stop or GracefulStop from there
// returns.
func TestServerStop_NestedStopFromAnUnservedListenersClose(t *testing.T) {
	for outerName, outer := range stopCalls {
		for nestedName, nestedCall := range stopCalls {
			t.Run(outerName+"/"+nestedName, func(t *testing.T) {
				nested := newNestedStop(nestedCall)
				lis := &nestedListener{Listener: testnet.Loopback(t), nested: nested}
				s := grpc.NewServer(grpc.WithListener(lis), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
				nested.server.Store(s)
				stopOnCleanup(t, s)
				if err := s.Build(); err != nil {
					t.Fatalf("Build: %v", err)
				}
				nested.armed.Store(true)
				checkNested(t, s, outer, nested, nestedName)
				if !lis.closed.Load() {
					t.Error("listener not closed")
				}
			})
		}
	}
}

// admittedCall starts a call, unary or stream, whose interceptor ignores
// its context until release is closed, and waits until it runs. done is
// set once the call's handler has returned.
func admittedCall(t *testing.T, s *grpc.Server, stream bool) (release chan struct{}, done *atomic.Bool) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	done = &atomic.Bool{}
	var once sync.Once
	if stream {
		s.UseStream(func(srv any, ss grpcgo.ServerStream, _ *grpcgo.StreamServerInfo, h grpcgo.StreamHandler) error {
			once.Do(func() { close(entered) })
			<-release
			defer done.Store(true)
			return h(srv, ss)
		})
	} else {
		s.Use(func(ctx context.Context, req any, _ *grpcgo.UnaryServerInfo, h grpcgo.UnaryHandler) (any, error) {
			once.Do(func() { close(entered) })
			<-release
			defer done.Store(true)
			return h(ctx, req)
		})
	}
	client := startHealth(t, s)
	go func() {
		if stream {
			if w, err := client.Watch(context.Background(), &grpc_health_v1.HealthCheckRequest{}); err == nil {
				for {
					if _, err := w.Recv(); err != nil {
						return
					}
				}
			}
			return
		}
		_, _ = client.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	}()
	select {
	case <-entered:
	case <-time.After(hostile.Deadline):
		t.Fatal("the call never reached the server")
	}
	return release, done
}

// Stop closes the transport without waiting for the handlers, but the
// server is not reported stopped while a call admitted before the stop
// still runs: ServerStopped comes, and a Shutdown returns nil, only once
// its handler has returned, for a unary call and a stream alike.
func TestServerStop_StoppedOnlyOnceTheAdmittedCallsReturn(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "unary"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			s := grpc.NewServer(grpc.WithListener(testnet.Loopback(t)), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
			var stopped, early atomic.Int32
			var done *atomic.Bool
			s.SetEventDispatcher(func(_ context.Context, ev any) error {
				if _, ok := ev.(*grpcevents.ServerStopped); ok {
					stopped.Add(1)
					if !done.Load() {
						early.Add(1)
					}
				}
				return nil
			})
			var release chan struct{}
			release, done = admittedCall(t, s, stream)

			if p := hostile.Within(t, hostile.Deadline, s.Stop); p != nil {
				t.Fatalf("Stop panicked: %v", p)
			}
			shut := make(chan error, 1)
			go func() { shut <- s.Shutdown(context.Background()) }()
			select {
			case err := <-shut:
				t.Fatalf("Shutdown returned %v while an admitted call still ran", err)
			case <-time.After(100 * time.Millisecond):
			}
			close(release)
			select {
			case err := <-shut:
				if err != nil {
					t.Errorf("Shutdown = %v, want nil", err)
				}
				if !done.Load() {
					t.Error("Shutdown returned before the call's handler did")
				}
			case <-time.After(hostile.Deadline):
				t.Fatal("Shutdown did not return after the call ended")
			}
			if n, e := stopped.Load(), early.Load(); n != 1 || e != 0 {
				t.Errorf("ServerStopped dispatched %d times, %d before the call ended: want once, after", n, e)
			}
		})
	}
}

// startLog records the starting line and the lifecycle events in order.
type startLog struct {
	mu     sync.Mutex
	events []string
}

func (l *startLog) add(what string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, what)
}

func (l *startLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

func (l *startLog) dispatch(_ context.Context, ev any) error {
	switch ev.(type) {
	case *grpcevents.ServerStarted:
		l.add("started")
	case *grpcevents.ServerStopped:
		l.add("stopped")
	}
	return nil
}

func (l *startLog) line(msg string) {
	if msg == "gRPC server starting" {
		l.add("line")
	}
}
func (l *startLog) Debug(msg string, _ ...any)  { l.line(msg) }
func (l *startLog) Info(msg string, _ ...any)   { l.line(msg) }
func (l *startLog) Warn(msg string, _ ...any)   { l.line(msg) }
func (l *startLog) Error(msg string, _ ...any)  { l.line(msg) }
func (l *startLog) Fatal(msg string, _ ...any)  { l.line(msg) }
func (l *startLog) With(...any) contract.Logger { return l }

// StartAsync publishes the start before it returns: the server reports
// running, ServerStarted has been dispatched and the starting line
// written, so a caller acting on its return never races them.
func TestServerStartAsync_PublishesTheStartBeforeReturning(t *testing.T) {
	for range 50 {
		log := &startLog{}
		s := grpc.NewServer(grpc.WithListener(testnet.Loopback(t)), grpc.WithLogger(log))
		s.SetEventDispatcher(log.dispatch)
		if err := s.StartAsync(); err != nil {
			t.Fatalf("StartAsync: %v", err)
		}
		got := log.all()
		running := s.IsRunning()
		s.Stop()
		if !running || len(got) != 2 {
			t.Fatalf("at StartAsync's return: running %v, published %v: want running, with ServerStarted and the starting line", running, got)
		}
	}
}

// A Stop right after StartAsync never reports the server stopped before
// it reported it started.
func TestServerStartAsync_ServerStartedPrecedesServerStopped(t *testing.T) {
	for range 200 {
		log := &startLog{}
		s := grpc.NewServer(grpc.WithListener(testnet.Loopback(t)), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
		s.SetEventDispatcher(log.dispatch)
		if err := s.StartAsync(); err != nil {
			t.Fatalf("StartAsync: %v", err)
		}
		s.Stop()
		hostile.Eventually(t, hostile.Deadline, "ServerStopped", func() bool {
			evs := log.all()
			return len(evs) > 0 && evs[len(evs)-1] == "stopped"
		})
		if got := log.all(); len(got) != 2 || got[0] != "started" {
			t.Fatalf("events = %v, want [started stopped]", got)
		}
	}
}

// addrPanicListener's Addr panics.
type addrPanicListener struct{ net.Listener }

func (addrPanicListener) Addr() net.Addr { panic("listener Addr broke") }

// grpc-go asks the listener for its address holding its own lock. A
// caller-supplied listener whose Addr panics neither kills the serve
// loop nor leaves that lock held: the server serves, reports an empty
// address, and stops.
func TestServer_ListenerAddrPanicIsContained(t *testing.T) {
	raw := testnet.Loopback(t)
	addr := raw.Addr().String()
	s := grpc.NewServer(grpc.WithListener(addrPanicListener{raw}), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
	s.RegisterService(regHealth)
	stopOnCleanup(t, s)
	var err error
	if p := hostile.Within(t, hostile.Deadline, func() { err = s.StartAsync() }); p != nil {
		t.Fatalf("StartAsync panicked: %v", p)
	}
	if err != nil {
		t.Fatalf("StartAsync: %v", err)
	}
	var got string
	if p := hostile.Within(t, hostile.Deadline, func() { got = s.Address() }); p != nil || got != "" {
		t.Errorf("Address = %q, panic %v: want an empty address", got, p)
	}
	checkHealth(t, addr)
	if p := hostile.Within(t, hostile.Deadline, s.Stop); p != nil {
		t.Fatalf("Stop panicked: %v", p)
	}
}

// acceptPanicListener's Accept panics.
type acceptPanicListener struct{ net.Listener }

func (acceptPanicListener) Accept() (net.Conn, error) { panic("listener Accept broke") }

// A caller-supplied listener whose Accept panics fails the serve loop
// like an Accept error: Start returns the failure instead of panicking,
// StartAsync returns nil once serving began, and the server is stopped,
// with ServerStopped dispatched once.
func TestServer_ListenerAcceptPanicStopsTheServer(t *testing.T) {
	for _, start := range []string{"Start", "StartAsync"} {
		t.Run(start, func(t *testing.T) {
			s := grpc.NewServer(grpc.WithListener(acceptPanicListener{testnet.Loopback(t)}), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
			counter := &stoppedCounter{}
			s.SetEventDispatcher(counter.dispatch)
			stopOnCleanup(t, s)
			var err error
			if p := hostile.Within(t, hostile.Deadline, func() {
				if start == "Start" {
					err = s.Start()
				} else {
					err = s.StartAsync()
				}
			}); p != nil {
				t.Fatalf("%s panicked: %v", start, p)
			}
			if start == "Start" && (err == nil || !strings.Contains(err.Error(), "Accept broke")) {
				t.Errorf("Start = %v, want the Accept failure", err)
			}
			if start == "StartAsync" && err != nil {
				t.Errorf("StartAsync = %v, want nil", err)
			}
			hostile.Eventually(t, hostile.Deadline, "ServerStopped", func() bool { return counter.n.Load() == 1 })
			ctx, cancel := context.WithTimeout(context.Background(), hostile.Deadline)
			defer cancel()
			if err := s.Shutdown(ctx); err != nil {
				t.Errorf("Shutdown = %v, want nil", err)
			}
			if s.IsRunning() {
				t.Error("server still running")
			}
		})
	}
}

// closePanicListener's Close closes the socket, then panics.
type closePanicListener struct{ net.Listener }

func (l closePanicListener) Close() error {
	_ = l.Listener.Close()
	panic("listener Close broke")
}

// grpc-go closes a served listener holding its own lock. A Close that
// panics there does not escape the stop, and leaves the stop to finish:
// Stop returns, ServerStopped is dispatched once and Shutdown returns nil.
func TestServer_ListenerClosePanicIsContained(t *testing.T) {
	s := grpc.NewServer(grpc.WithListener(closePanicListener{testnet.Loopback(t)}), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
	counter := &stoppedCounter{}
	s.SetEventDispatcher(counter.dispatch)
	served(t, startHealth(t, s))
	if p := hostile.Within(t, hostile.Deadline, s.Stop); p != nil {
		t.Fatalf("Stop panicked: %v", p)
	}
	ctx, cancel := context.WithTimeout(context.Background(), hostile.Deadline)
	defer cancel()
	var err error
	if p := hostile.Within(t, hostile.Deadline, func() { err = s.Shutdown(ctx) }); p != nil {
		t.Fatalf("Shutdown panicked: %v", p)
	}
	if err != nil {
		t.Errorf("Shutdown = %v, want nil", err)
	}
	if n := counter.n.Load(); n != 1 {
		t.Errorf("ServerStopped dispatched %d times, want 1", n)
	}
}

func regHealth(srv any) {
	grpc_health_v1.RegisterHealthServer(srv.(*grpcgo.Server), health.NewServer())
}

// checkHealth fails the test unless a health check to addr succeeds.
func checkHealth(t *testing.T, addr string) {
	t.Helper()
	conn, err := grpcgo.NewClient(addr, grpcgo.WithTransportCredentials(insecure.NewCredentials()))
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
