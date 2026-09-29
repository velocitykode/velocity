package grpc

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc/grpcevents"
	"github.com/velocitykode/velocity/grpc/interceptors"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/trace"
)

const (
	chainDoMethod    = "/velocity.test.Chain/Do"
	chainWatchMethod = "/velocity.test.Chain/Watch"
)

// chainDesc is a hand-written descriptor for a unary Do and a
// server-streaming Watch, both on google.protobuf.Empty.
var chainDesc = grpcgo.ServiceDesc{
	ServiceName: "velocity.test.Chain",
	HandlerType: (*any)(nil),
	Methods: []grpcgo.MethodDesc{{
		MethodName: "Do",
		Handler: func(srv any, ctx context.Context, dec func(any) error, interceptor grpcgo.UnaryServerInterceptor) (any, error) {
			in := new(emptypb.Empty)
			if err := dec(in); err != nil {
				return nil, err
			}
			h := func(ctx context.Context, _ any) (any, error) { return &emptypb.Empty{}, srv.(*chainServer).run(ctx) }
			if interceptor == nil {
				return h(ctx, in)
			}
			return interceptor(ctx, in, &grpcgo.UnaryServerInfo{Server: srv, FullMethod: chainDoMethod}, h)
		},
	}},
	Streams: []grpcgo.StreamDesc{{
		StreamName:    "Watch",
		ServerStreams: true,
		Handler: func(srv any, stream grpcgo.ServerStream) error {
			in := new(emptypb.Empty)
			if err := stream.RecvMsg(in); err != nil {
				return err
			}
			return srv.(*chainServer).run(stream.Context())
		},
	}},
}

// chainServer fails every call the way its outcome says (a returned
// Internal error, or a panic; for "interceptor panic" the handler succeeds
// and an interceptor after it panics) and records the ids its handler ran
// under.
type chainServer struct {
	outcome string

	mu        sync.Mutex
	requestID string
	traceID   string
	spanID    string
}

func (c *chainServer) run(ctx context.Context) error {
	c.mu.Lock()
	c.requestID, c.traceID, c.spanID = trace.GetRequestID(ctx), trace.GetTraceID(ctx), trace.GetSpanID(ctx)
	c.mu.Unlock()
	switch c.outcome {
	case "panic":
		panic("handler broke")
	case "interceptor panic":
		return nil
	}
	return status.Error(codes.Internal, "handler failed")
}

func (c *chainServer) ids() (string, string, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requestID, c.traceID, c.spanID
}

// chainReports records every report the server's reporter receives.
type chainReports struct {
	mu   sync.Mutex
	errs []error
	ecs  []*contract.ErrorContext
}

func (r *chainReports) Report(err error, ec *contract.ErrorContext) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, err)
	r.ecs = append(r.ecs, ec)
}

func (r *chainReports) all() ([]error, []*contract.ErrorContext) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]error(nil), r.errs...), append([]*contract.ErrorContext(nil), r.ecs...)
}

// TestServerChain_FailedCallEndsWithItsEventsAndCorrelatedReport runs a
// unary call and a stream through a framework-built server (its default
// call lifecycle interceptor with its request line) whose handler returns an Internal
// error or panics, or whose user interceptor panics after the handler. Every such call ends with its failed and completed
// events, and is reported once, under the request, trace and span ids the
// handler and the call lifecycle interceptor's request line saw.
func TestServerChain_FailedCallEndsWithItsEventsAndCorrelatedReport(t *testing.T) {
	for _, kind := range []string{"unary", "stream"} {
		for _, outcome := range []string{"error", "panic", "interceptor panic"} {
			t.Run(kind+"/"+outcome, func(t *testing.T) {
				srv := &chainServer{outcome: outcome}
				reports := &chainReports{}
				lines := &requestLog{}
				evs := &grpcEventLog{}

				lis, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatalf("listen: %v", err)
				}
				quiet, _ := log.NewLogger(log.LogConfig{Driver: "null"})
				s := NewServer(WithListener(lis), WithLogger(quiet), WithEnvironment("testing"), WithReporter(reports), WithCallOptions(
					interceptors.WithRequestLine(),
					interceptors.WithLogger(lines),
					interceptors.WithEventDispatcher(evs.dispatch),
				))
				s.MarkAuthConfigured()
				if outcome == "interceptor panic" {
					s.Use(func(ctx context.Context, req any, _ *grpcgo.UnaryServerInfo, h grpcgo.UnaryHandler) (any, error) {
						_, _ = h(ctx, req)
						panic("interceptor broke")
					})
					s.UseStream(func(srv any, ss grpcgo.ServerStream, _ *grpcgo.StreamServerInfo, h grpcgo.StreamHandler) error {
						_ = h(srv, ss)
						panic("interceptor broke")
					})
				}
				s.RegisterService(func(g interface{}) {
					g.(*grpcgo.Server).RegisterService(&chainDesc, srv)
				})
				if err := s.StartAsync(); err != nil {
					t.Fatalf("StartAsync: %v", err)
				}
				t.Cleanup(func() {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					_ = s.Shutdown(ctx)
				})
				conn, err := grpcgo.NewClient(lis.Addr().String(), grpcgo.WithTransportCredentials(insecure.NewCredentials()))
				if err != nil {
					t.Fatalf("client: %v", err)
				}
				t.Cleanup(func() { _ = conn.Close() })

				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				var callErr error
				if kind == "unary" {
					callErr = conn.Invoke(ctx, chainDoMethod, &emptypb.Empty{}, &emptypb.Empty{})
				} else {
					st, err := conn.NewStream(ctx, &chainDesc.Streams[0], chainWatchMethod)
					if err != nil {
						t.Fatalf("NewStream: %v", err)
					}
					if err := st.SendMsg(&emptypb.Empty{}); err != nil {
						t.Fatalf("SendMsg: %v", err)
					}
					_ = st.CloseSend()
					callErr = st.RecvMsg(&emptypb.Empty{})
				}
				if got := status.Code(callErr); got != codes.Internal {
					t.Fatalf("client code = %v (%v), want Internal", got, callErr)
				}

				var verbs []string
				var terminal codes.Code = codes.OK
				evs.mu.Lock()
				for _, ev := range evs.events {
					switch e := ev.(type) {
					case *grpcevents.RequestStarted, *grpcevents.StreamStarted:
						verbs = append(verbs, "started")
					case *grpcevents.RequestFailed, *grpcevents.StreamFailed:
						verbs = append(verbs, "failed")
					case *grpcevents.RequestCompleted:
						verbs = append(verbs, "completed")
						terminal = e.StatusCode
					case *grpcevents.StreamCompleted:
						verbs = append(verbs, "completed")
						terminal = e.StatusCode
					}
				}
				evs.mu.Unlock()
				if want := []string{"started", "failed", "completed"}; !equalStrings(verbs, want) {
					t.Errorf("lifecycle events = %v, want %v", verbs, want)
				}
				if terminal != codes.Internal {
					t.Errorf("completed event StatusCode = %v, want Internal", terminal)
				}

				errs, ecs := reports.all()
				if len(errs) != 1 {
					t.Fatalf("reports = %d (%v), want 1", len(errs), errs)
				}
				reqID, traceID, spanID := srv.ids()
				if reqID == "" || traceID == "" || spanID == "" {
					t.Fatalf("handler ran without ids: request %q trace %q span %q", reqID, traceID, spanID)
				}
				ec := ecs[0]
				if ec.RequestID != reqID || ec.TraceID != traceID || ec.SpanID != spanID {
					t.Errorf("report ids = request %q trace %q span %q, want the handler's %q %q %q",
						ec.RequestID, ec.TraceID, ec.SpanID, reqID, traceID, spanID)
				}
				if got := lines.last(); got != reqID {
					t.Errorf("logging line request_id = %q, want the handler's %q", got, reqID)
				}
				panicked := outcome != "error"
				if ec.Recovered != panicked {
					t.Errorf("report Recovered = %v, want %v", ec.Recovered, panicked)
				}
				var pe contract.RecoveredPanic
				if panicked && !errors.As(errs[0], &pe) {
					t.Errorf("reported panic %v is not a recovered panic", errs[0])
				}
			})
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
