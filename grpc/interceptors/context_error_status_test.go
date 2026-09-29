package interceptors_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc/grpcevents"
	"github.com/velocitykode/velocity/grpc/interceptors"
)

// statusEvents records the StatusCode of every terminal event.
type statusEvents struct {
	mu    sync.Mutex
	codes []codes.Code
}

func (s *statusEvents) dispatch(_ context.Context, ev any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch e := ev.(type) {
	case *grpcevents.RequestFailed:
		s.codes = append(s.codes, e.StatusCode)
	case *grpcevents.RequestCompleted:
		s.codes = append(s.codes, e.StatusCode)
	case *grpcevents.StreamFailed:
		s.codes = append(s.codes, e.StatusCode)
	case *grpcevents.StreamCompleted:
		s.codes = append(s.codes, e.StatusCode)
	}
	return nil
}

// ctxErrorReports counts the reports it receives.
type ctxErrorReports struct {
	mu sync.Mutex
	n  int
}

func (r *ctxErrorReports) Report(error, *contract.ErrorContext) {
	r.mu.Lock()
	r.n++
	r.mu.Unlock()
}

func (r *ctxErrorReports) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// A handler that returns a context error, directly or wrapped, ends the
// call with the status grpc-go sends for it (Canceled or DeadlineExceeded):
// the failed and completed events and the logging line carry that code, and
// recovery does not report it as an internal error.
func TestCallLifecycle_ContextErrorsGetTheirStatus(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"canceled", context.Canceled, codes.Canceled},
		{"deadline", context.DeadlineExceeded, codes.DeadlineExceeded},
		{"wrapped canceled", fmt.Errorf("query: %w", context.Canceled), codes.Canceled},
		{"wrapped deadline", fmt.Errorf("query: %w", context.DeadlineExceeded), codes.DeadlineExceeded},
	}
	for _, tc := range cases {
		for _, kind := range []string{"unary", "stream"} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				evs := &statusEvents{}
				lines := newBoundLogger()
				reports := &ctxErrorReports{}
				logging := interceptors.CallLifecycle(interceptors.WithRequestLine(),
					interceptors.WithLogger(lines),
					interceptors.WithEventDispatcher(evs.dispatch),
				)
				rec := interceptors.CallLifecycle(interceptors.WithReporter(reports))
				if kind == "unary" {
					_, _ = rec.Unary(context.Background(), nil, mockUnaryServerInfo("/svc.Work/Do"),
						func(ctx context.Context, req any) (any, error) {
							return logging.Unary(ctx, req, mockUnaryServerInfo("/svc.Work/Do"),
								func(context.Context, any) (any, error) { return nil, tc.err })
						})
				} else {
					_ = rec.Stream(nil, &mockServerStream{ctx: context.Background()}, mockStreamServerInfo("/svc.Work/Watch"),
						func(srv any, ss grpc.ServerStream) error {
							return logging.Stream(srv, ss, mockStreamServerInfo("/svc.Work/Watch"),
								func(any, grpc.ServerStream) error { return tc.err })
						})
				}
				evs.mu.Lock()
				got := append([]codes.Code(nil), evs.codes...)
				evs.mu.Unlock()
				if len(got) != 2 || got[0] != tc.want || got[1] != tc.want {
					t.Errorf("failed and completed StatusCode = %v, want [%v %v]", got, tc.want, tc.want)
				}
				if code := lines.last(t)["code"]; code != tc.want.String() {
					t.Errorf("logging line code = %v, want %v", code, tc.want)
				}
				if n := reports.count(); n != 0 {
					t.Errorf("reports = %d, want 0 (the client gets %v, not an internal error)", n, tc.want)
				}
			})
		}
	}
}
