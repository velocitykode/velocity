package grpc

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/velocitykode/velocity/grpc/grpcevents"
	"github.com/velocitykode/velocity/grpc/interceptors"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/trace"
)

const echoPingMethod = "/velocity.test.Echo/Ping"

// echoDesc is a hand-written service descriptor for a unary Ping that takes
// and returns google.protobuf.Empty, so the tests need no generated code.
var echoDesc = grpcgo.ServiceDesc{
	ServiceName: "velocity.test.Echo",
	HandlerType: (*any)(nil),
	Methods: []grpcgo.MethodDesc{{
		MethodName: "Ping",
		Handler: func(srv any, ctx context.Context, dec func(any) error, interceptor grpcgo.UnaryServerInterceptor) (any, error) {
			in := new(emptypb.Empty)
			if err := dec(in); err != nil {
				return nil, err
			}
			h := func(ctx context.Context, _ any) (any, error) { return srv.(*echoServer).ping(ctx) }
			if interceptor == nil {
				return h(ctx, in)
			}
			return interceptor(ctx, in, &grpcgo.UnaryServerInfo{Server: srv, FullMethod: echoPingMethod}, h)
		},
	}},
}

// echoServer records the trace ids its handler observed.
type echoServer struct {
	mu      sync.Mutex
	traceID string
	spanID  string
}

func (e *echoServer) ping(ctx context.Context) (any, error) {
	e.mu.Lock()
	e.traceID, e.spanID = trace.GetTraceID(ctx), trace.GetSpanID(ctx)
	e.mu.Unlock()
	return &emptypb.Empty{}, nil
}

// requestLog records the request_id field of every line the logging
// interceptor writes.
type requestLog struct {
	mu  sync.Mutex
	ids []string
}

func (l *requestLog) record(kvs []any) {
	for i := 0; i+1 < len(kvs); i += 2 {
		if kvs[i] == "request_id" {
			id, _ := kvs[i+1].(string)
			l.mu.Lock()
			l.ids = append(l.ids, id)
			l.mu.Unlock()
		}
	}
}

func (l *requestLog) Debug(_ string, kvs ...any) { l.record(kvs) }
func (l *requestLog) Info(_ string, kvs ...any)  { l.record(kvs) }
func (l *requestLog) Warn(_ string, kvs ...any)  { l.record(kvs) }
func (l *requestLog) Error(_ string, kvs ...any) { l.record(kvs) }
func (l *requestLog) Fatal(_ string, kvs ...any) { l.record(kvs) }

func (l *requestLog) last() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.ids) == 0 {
		return ""
	}
	return l.ids[len(l.ids)-1]
}

type grpcEventLog struct {
	mu     sync.Mutex
	events []any
}

func (l *grpcEventLog) dispatch(_ context.Context, ev any) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, ev)
	return nil
}

func (l *grpcEventLog) started() *grpcevents.RequestStarted {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := len(l.events) - 1; i >= 0; i-- {
		if e, ok := l.events[i].(*grpcevents.RequestStarted); ok {
			return e
		}
	}
	return nil
}

// echoRig is a running velocity gRPC server with the logging interceptor
// wired to a request log and an event log.
type echoRig struct {
	addr   string
	echo   *echoServer
	logs   *requestLog
	events *grpcEventLog
}

func startEchoRig(t *testing.T) *echoRig {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	quiet, _ := log.NewLogger(log.LogConfig{Driver: "null"})
	rig := &echoRig{addr: lis.Addr().String(), echo: &echoServer{}, logs: &requestLog{}, events: &grpcEventLog{}}
	s := NewServer(WithListener(lis), WithLogger(quiet), WithEnvironment("testing"))
	s.UseAll(interceptors.Logging(
		interceptors.WithLoggingLogger(rig.logs),
		interceptors.WithEventDispatcher(rig.events.dispatch),
	))
	s.MarkAuthConfigured()
	s.RegisterService(func(srv interface{}) {
		srv.(*grpcgo.Server).RegisterService(&echoDesc, rig.echo)
	})
	if err := s.StartAsync(); err != nil {
		t.Fatalf("StartAsync: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})
	return rig
}

// buildEchoGateway builds a gateway in front of rig whose GET /v1/ping calls
// Echo/Ping the way grpc-gateway's generated handlers do: AnnotateContext on
// the request context, then a unary call on a client built from the dial
// options the gateway hands its registrations.
func buildEchoGateway(t *testing.T, rig *echoRig) http.Handler {
	t.Helper()
	quiet, _ := log.NewLogger(log.LogConfig{Driver: "null"})
	g := NewGateway(
		GatewayWithGRPCEndpoint(rig.addr),
		GatewayWithInsecure(),
		GatewayWithLogger(quiet),
	)
	g.RegisterHandler(func(ctx context.Context, mux *runtime.ServeMux, endpoint string, opts []grpcgo.DialOption) error {
		conn, err := grpcgo.NewClient(endpoint, opts...)
		if err != nil {
			return err
		}
		t.Cleanup(func() { _ = conn.Close() })
		return mux.HandlePath(http.MethodGet, "/v1/ping", func(w http.ResponseWriter, r *http.Request, _ map[string]string) {
			actx, err := runtime.AnnotateContext(r.Context(), mux, r, echoPingMethod)
			if err != nil {
				http.Error(w, "annotate", http.StatusBadRequest)
				return
			}
			if err := conn.Invoke(actx, echoPingMethod, &emptypb.Empty{}, &emptypb.Empty{}); err != nil {
				http.Error(w, "invoke", http.StatusBadGateway)
				return
			}
			w.WriteHeader(http.StatusOK)
		})
	})
	if err := g.Build(context.Background()); err != nil {
		t.Fatalf("gateway Build: %v", err)
	}
	return g.httpServer.Handler
}

var twentyHex = regexp.MustCompile(`^[0-9a-f]{20}$`)

// TestGatewayCall_OneRequestIDAcrossHTTPAndGRPC pins that the HTTP and gRPC
// halves of a gateway call carry one request id: the one the HTTP caller
// sent when it is usable, otherwise one the gateway generates, echoed on the
// HTTP response and seen by the gRPC server.
func TestGatewayCall_OneRequestIDAcrossHTTPAndGRPC(t *testing.T) {
	rig := startEchoRig(t)
	handler := buildEchoGateway(t, rig)

	t.Run("inbound id is kept", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/ping", nil)
		req.Header.Set("X-Request-ID", "edge-4f1c9a")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("X-Request-ID"); got != "edge-4f1c9a" {
			t.Errorf("HTTP response X-Request-ID = %q, want %q", got, "edge-4f1c9a")
		}
		if got := rig.logs.last(); got != "edge-4f1c9a" {
			t.Errorf("gRPC server request_id = %q, want %q", got, "edge-4f1c9a")
		}
	})

	t.Run("gateway generates one", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/ping", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
		}
		id := rec.Header().Get("X-Request-ID")
		if !twentyHex.MatchString(id) {
			t.Fatalf("HTTP response X-Request-ID = %q, want a generated 20-hex id", id)
		}
		if got := rig.logs.last(); got != id {
			t.Errorf("gRPC server request_id = %q, want the gateway's %q", got, id)
		}
	})

	t.Run("unusable inbound id is replaced", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/ping", nil)
		req.Header.Set("X-Request-ID", "has space")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		id := rec.Header().Get("X-Request-ID")
		if !twentyHex.MatchString(id) {
			t.Fatalf("HTTP response X-Request-ID = %q, want a generated 20-hex id", id)
		}
		if got := rig.logs.last(); got != id {
			t.Errorf("gRPC server request_id = %q, want the gateway's %q", got, id)
		}
	})
}

// TestGatewayCall_ForwardsTraceparent pins that the gateway, which records no
// span of its own, hands the HTTP caller's span to the gRPC server as the
// parent: the server's events carry the caller's trace id and ParentID equal
// to the caller's span.
func TestGatewayCall_ForwardsTraceparent(t *testing.T) {
	rig := startEchoRig(t)
	handler := buildEchoGateway(t, rig)

	callerTrace, callerSpan := "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"
	req := httptest.NewRequest(http.MethodGet, "/v1/ping", nil)
	req.Header.Set("traceparent", "00-"+callerTrace+"-"+callerSpan+"-01")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
	}

	started := rig.events.started()
	if started == nil {
		t.Fatal("RequestStarted not dispatched")
	}
	if started.TraceID != callerTrace {
		t.Errorf("RequestStarted.TraceID = %q, want the caller's %q", started.TraceID, callerTrace)
	}
	if started.ParentID != callerSpan {
		t.Errorf("RequestStarted.ParentID = %q, want the caller's span %q", started.ParentID, callerSpan)
	}
	if started.SpanID == "" || started.SpanID == callerSpan {
		t.Errorf("RequestStarted.SpanID = %q, want a new span", started.SpanID)
	}
	rig.echo.mu.Lock()
	defer rig.echo.mu.Unlock()
	if rig.echo.traceID != callerTrace || rig.echo.spanID != started.SpanID {
		t.Errorf("handler ran in trace %q span %q, want trace %q span %q", rig.echo.traceID, rig.echo.spanID, callerTrace, started.SpanID)
	}
}

// TestClientCall_TraceparentReachesServerEvents covers a direct gRPC call
// from a client built with the Propagation interceptors: the call carries
// traceparent and x-request-id in metadata, and the server's events report
// the caller's trace id with ParentID equal to the caller's span.
func TestClientCall_TraceparentReachesServerEvents(t *testing.T) {
	rig := startEchoRig(t)
	p := interceptors.Propagation()
	conn, err := grpcgo.NewClient(rig.addr,
		grpcgo.WithTransportCredentials(insecure.NewCredentials()),
		grpcgo.WithChainUnaryInterceptor(p.Unary),
		grpcgo.WithChainStreamInterceptor(p.Stream),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer conn.Close()

	callerTrace, callerSpan := "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"
	ctx := trace.WithRequestID(trace.WithTrace(context.Background(), callerTrace, callerSpan), "caller-req-1")
	if err := conn.Invoke(ctx, echoPingMethod, &emptypb.Empty{}, &emptypb.Empty{}); err != nil {
		t.Fatalf("Invoke: %v", err)
	}

	started := rig.events.started()
	if started == nil {
		t.Fatal("RequestStarted not dispatched")
	}
	if started.TraceID != callerTrace {
		t.Errorf("RequestStarted.TraceID = %q, want the caller's %q", started.TraceID, callerTrace)
	}
	if started.ParentID != callerSpan {
		t.Errorf("RequestStarted.ParentID = %q, want the caller's span %q", started.ParentID, callerSpan)
	}
	if started.SpanID == "" || started.SpanID == callerSpan {
		t.Errorf("RequestStarted.SpanID = %q, want a new span", started.SpanID)
	}
	if got := rig.logs.last(); got != "caller-req-1" {
		t.Errorf("server request_id = %q, want the caller's %q", got, "caller-req-1")
	}
}
