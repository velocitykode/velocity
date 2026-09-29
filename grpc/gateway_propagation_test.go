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
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/velocitykode/velocity/contract"
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

// echoServer records the trace ids and request id its handler observed and
// the carriers the call's incoming metadata held.
type echoServer struct {
	mu          sync.Mutex
	traceID     string
	spanID      string
	requestID   string
	traceparent []string
	xRequestID  []string
}

func (e *echoServer) ping(ctx context.Context) (any, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	e.mu.Lock()
	e.traceID, e.spanID = trace.GetTraceID(ctx), trace.GetSpanID(ctx)
	e.requestID = trace.GetRequestID(ctx)
	e.traceparent = md.Get(trace.TraceparentHeader)
	e.xRequestID = md.Get(trace.RequestIDHeader)
	e.mu.Unlock()
	return &emptypb.Empty{}, nil
}

// seen returns what the handler recorded on its last call.
func (e *echoServer) seen() (requestID string, traceparent, xRequestID []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.requestID, e.traceparent, e.xRequestID
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

func (l *requestLog) With(kvs ...any) contract.Logger { return contract.BindFields(l, kvs...) }

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

// echoRig is a running velocity gRPC server with the call lifecycle interceptor
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
	s := NewServer(WithListener(lis), WithLogger(quiet), WithEnvironment("testing"), WithCallOptions(
		interceptors.WithRequestLine(),
		interceptors.WithLogger(rig.logs),
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
// options the gateway hands its registrations. middleware is installed with
// Gateway.Use.
func buildEchoGateway(t *testing.T, rig *echoRig, middleware ...func(http.Handler) http.Handler) http.Handler {
	t.Helper()
	quiet, _ := log.NewLogger(log.LogConfig{Driver: "null"})
	g := NewGateway(
		GatewayWithGRPCEndpoint(rig.addr),
		GatewayWithInsecure(),
		GatewayWithLogger(quiet),
	)
	g.Use(middleware...)
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

// TestGatewayCall_SelectedRequestIDWinsOverPrefixedMetadata pins that the
// request id the gateway selects (and echoes) is the one the proxied gRPC
// call carries, whatever Grpc-Metadata-X-Request-ID headers the HTTP caller
// sent: grpc-gateway turns those into outgoing metadata, and a conflicting,
// repeated or invalid one must not reach the gRPC server in place of the
// echoed id. The response header, the handler's context and the server's log
// line all see one id, and the incoming metadata holds exactly that one.
func TestGatewayCall_SelectedRequestIDWinsOverPrefixedMetadata(t *testing.T) {
	rig := startEchoRig(t)
	handler := buildEchoGateway(t, rig)

	for _, tc := range []struct {
		name     string
		primary  []string // X-Request-ID values
		prefixed []string // Grpc-Metadata-X-Request-ID values
		want     string   // the id every half must see; "" means a generated one
	}{
		{name: "conflicting prefixed id", primary: []string{"public-id"}, prefixed: []string{"backend-id"}, want: "public-id"},
		{name: "repeated prefixed id", primary: []string{"public-id"}, prefixed: []string{"backend-a", "backend-b"}, want: "public-id"},
		{name: "invalid prefixed id", primary: []string{"public-id"}, prefixed: []string{"has space"}, want: "public-id"},
		{name: "rejected primary id", primary: []string{"has space"}, prefixed: []string{"backend-id"}},
		{name: "repeated primary id", primary: []string{"public-a", "public-b"}, prefixed: []string{"backend-id"}},
		{name: "prefixed id only", prefixed: []string{"backend-id"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/ping", nil)
			for _, v := range tc.primary {
				req.Header.Add("X-Request-ID", v)
			}
			for _, v := range tc.prefixed {
				req.Header.Add("Grpc-Metadata-X-Request-ID", v)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
			}

			echoed := rec.Header().Get("X-Request-ID")
			if tc.want != "" && echoed != tc.want {
				t.Fatalf("HTTP response X-Request-ID = %q, want %q", echoed, tc.want)
			}
			if tc.want == "" && !twentyHex.MatchString(echoed) {
				t.Fatalf("HTTP response X-Request-ID = %q, want a generated 20-hex id", echoed)
			}
			handlerID, _, mdIDs := rig.echo.seen()
			if handlerID != echoed {
				t.Errorf("gRPC handler request id = %q, want the echoed %q", handlerID, echoed)
			}
			if got := rig.logs.last(); got != echoed {
				t.Errorf("gRPC server log request_id = %q, want the echoed %q", got, echoed)
			}
			if len(mdIDs) != 1 || mdIDs[0] != echoed {
				t.Errorf("incoming x-request-id metadata = %q, want only the echoed %q", mdIDs, echoed)
			}
		})
	}
}

// TestGatewayCall_SelectedTraceWinsOverPrefixedMetadata pins that the trace
// the gateway selects from the traceparent header is the one the proxied
// gRPC call carries: a Grpc-Metadata-Traceparent header neither overrides an
// accepted traceparent nor stands in for one the gateway rejected (absent,
// malformed or repeated), in which case the gRPC server starts a root span.
func TestGatewayCall_SelectedTraceWinsOverPrefixedMetadata(t *testing.T) {
	rig := startEchoRig(t)
	handler := buildEchoGateway(t, rig)

	callerTrace, callerSpan := "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"
	caller := "00-" + callerTrace + "-" + callerSpan + "-01"
	otherTrace, otherSpan := "0af7651916cd43dd8448eb211c80319c", "b7ad6b7169203331"
	other := "00-" + otherTrace + "-" + otherSpan + "-01"

	for _, tc := range []struct {
		name      string
		primary   []string // traceparent values
		prefixed  []string // Grpc-Metadata-Traceparent values
		continues bool     // the server continues the caller's trace; false means a root span
	}{
		{name: "conflicting prefixed traceparent", primary: []string{caller}, prefixed: []string{other}, continues: true},
		{name: "repeated prefixed traceparent", primary: []string{caller}, prefixed: []string{other, other}, continues: true},
		{name: "invalid prefixed traceparent", primary: []string{caller}, prefixed: []string{"00-garbage"}, continues: true},
		{name: "rejected primary traceparent", primary: []string{"00-garbage"}, prefixed: []string{other}},
		{name: "repeated primary traceparent", primary: []string{caller, caller}, prefixed: []string{other}},
		{name: "prefixed traceparent only", prefixed: []string{other}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/ping", nil)
			for _, v := range tc.primary {
				req.Header.Add("traceparent", v)
			}
			for _, v := range tc.prefixed {
				req.Header.Add("Grpc-Metadata-Traceparent", v)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
			}

			started := rig.events.started()
			if started == nil {
				t.Fatal("RequestStarted not dispatched")
			}
			_, mdTraceparent, _ := rig.echo.seen()
			if tc.continues {
				if started.TraceID != callerTrace || started.ParentID != callerSpan {
					t.Errorf("RequestStarted trace %q parent %q, want the caller's trace %q parent %q", started.TraceID, started.ParentID, callerTrace, callerSpan)
				}
				if len(mdTraceparent) != 1 || mdTraceparent[0] != caller {
					t.Errorf("incoming traceparent metadata = %q, want only the caller's %q", mdTraceparent, caller)
				}
				return
			}
			if started.TraceID == otherTrace || started.TraceID == callerTrace || started.ParentID != "" {
				t.Errorf("RequestStarted trace %q parent %q, want a root span", started.TraceID, started.ParentID)
			}
			if len(mdTraceparent) != 0 {
				t.Errorf("incoming traceparent metadata = %q, want none", mdTraceparent)
			}
		})
	}
}

// TestGatewayCall_KeepsSampledFlagOfForwardedSpan pins that the gateway,
// which forwards the HTTP caller's span unchanged, forwards its sampled flag
// unchanged too: an unsampled inbound traceparent stays unsampled on the
// proxied call. A span application middleware starts inside the gateway is
// a new span, sent as sampled.
func TestGatewayCall_KeepsSampledFlagOfForwardedSpan(t *testing.T) {
	callerTrace, callerSpan := "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"

	t.Run("forwarded span", func(t *testing.T) {
		rig := startEchoRig(t)
		handler := buildEchoGateway(t, rig)
		for _, flags := range []string{"00", "01"} {
			inbound := "00-" + callerTrace + "-" + callerSpan + "-" + flags
			req := httptest.NewRequest(http.MethodGet, "/v1/ping", nil)
			req.Header.Set("traceparent", inbound)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
			}
			if _, got, _ := rig.echo.seen(); len(got) != 1 || got[0] != inbound {
				t.Errorf("flags %s: outgoing traceparent = %q, want %q", flags, got, inbound)
			}
		}
	})

	t.Run("span started by middleware", func(t *testing.T) {
		rig := startEchoRig(t)
		var spanID string
		handler := buildEchoGateway(t, rig, func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx, id := trace.ContinueTrace(r.Context())
				spanID = id
				next.ServeHTTP(w, r.WithContext(ctx))
			})
		})
		req := httptest.NewRequest(http.MethodGet, "/v1/ping", nil)
		req.Header.Set("traceparent", "00-"+callerTrace+"-"+callerSpan+"-00")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
		}
		want := "00-" + callerTrace + "-" + spanID + "-01"
		if _, got, _ := rig.echo.seen(); len(got) != 1 || got[0] != want {
			t.Errorf("outgoing traceparent = %q, want the middleware's span %q", got, want)
		}
	})
}

// TestGatewayPropagation_StreamAndCallsOutsideGatewayRequests pins the
// gateway client interceptors directly: on a stream opened for a gateway
// request the selected carriers replace conflicting outgoing metadata and
// every other key is kept; on a call through the same client outside a
// gateway request, metadata the caller set wins, as with
// interceptors.Propagation.
func TestGatewayPropagation_StreamAndCallsOutsideGatewayRequests(t *testing.T) {
	callerTrace, callerSpan := "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"
	caller := "00-" + callerTrace + "-" + callerSpan + "-00"
	other := "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	p := gatewayPropagation()

	t.Run("stream for a gateway request", func(t *testing.T) {
		var gatewayCtx context.Context
		req := httptest.NewRequest(http.MethodGet, "/v1/stream", nil)
		req.Header.Set("X-Request-ID", "public-id")
		req.Header.Set("traceparent", caller)
		correlateGatewayRequest(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			gatewayCtx = r.Context()
		})).ServeHTTP(httptest.NewRecorder(), req)

		ctx := metadata.AppendToOutgoingContext(gatewayCtx,
			"x-request-id", "backend-id", "traceparent", other, "authorization", "Bearer t")
		var seen metadata.MD
		streamer := func(ctx context.Context, _ *grpcgo.StreamDesc, _ *grpcgo.ClientConn, _ string, _ ...grpcgo.CallOption) (grpcgo.ClientStream, error) {
			seen, _ = metadata.FromOutgoingContext(ctx)
			return nil, nil
		}
		if _, err := p.Stream(ctx, &grpcgo.StreamDesc{}, nil, "/svc/S", streamer); err != nil {
			t.Fatal(err)
		}
		if got := seen.Get("x-request-id"); len(got) != 1 || got[0] != "public-id" {
			t.Errorf("x-request-id = %q, want only %q", got, "public-id")
		}
		if got := seen.Get("traceparent"); len(got) != 1 || got[0] != caller {
			t.Errorf("traceparent = %q, want only %q", got, caller)
		}
		if got := seen.Get("authorization"); len(got) != 1 || got[0] != "Bearer t" {
			t.Errorf("authorization = %q, want it kept", got)
		}
	})

	t.Run("call outside a gateway request", func(t *testing.T) {
		ctx := trace.WithRequestID(trace.WithTrace(context.Background(), callerTrace, callerSpan), "ctx-id")
		ctx = metadata.AppendToOutgoingContext(ctx, "x-request-id", "caller-id", "traceparent", other)
		var seen metadata.MD
		invoker := func(ctx context.Context, _ string, _, _ any, _ *grpcgo.ClientConn, _ ...grpcgo.CallOption) error {
			seen, _ = metadata.FromOutgoingContext(ctx)
			return nil
		}
		if err := p.Unary(ctx, "/svc/M", nil, nil, nil, invoker); err != nil {
			t.Fatal(err)
		}
		if got := seen.Get("x-request-id"); len(got) != 1 || got[0] != "caller-id" {
			t.Errorf("x-request-id = %q, want the caller's %q", got, "caller-id")
		}
		if got := seen.Get("traceparent"); len(got) != 1 || got[0] != other {
			t.Errorf("traceparent = %q, want the caller's %q", got, other)
		}
	})
}
