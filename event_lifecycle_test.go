package velocity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/velocitykode/velocity/grpc/grpcevents"
	"github.com/velocitykode/velocity/grpc/interceptors"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/router"
)

// eventNameRecorder records the name of every event it is handed.
type eventNameRecorder struct {
	mu     sync.Mutex
	names  []string
	events []any
}

func (r *eventNameRecorder) dispatch(_ context.Context, event any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if named, ok := event.(interface{ Name() string }); ok {
		r.names = append(r.names, named.Name())
	}
	r.events = append(r.events, event)
	return nil
}

// verbs returns the last segment of every recorded name except routed,
// which only HTTP has.
func (r *eventNameRecorder) verbs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, n := range r.names {
		verb := n[strings.LastIndex(n, ".")+1:]
		if verb != "routed" {
			out = append(out, verb)
		}
	}
	return out
}

// TestRequestLifecycle_HTTPAndGRPCFollowOneRule serves an HTTP request and a
// gRPC call and stream that fail on the server and ones that succeed: each
// produces started, then failed when it failed, then completed, the
// terminal event for every request.
func TestRequestLifecycle_HTTPAndGRPCFollowOneRule(t *testing.T) {
	tests := []struct {
		name      string
		fail      bool
		wantVerbs []string
	}{
		{"server failure", true, []string{"started", "failed", "completed"}},
		{"success", false, []string{"started", "completed"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			httpEvents := &eventNameRecorder{}
			r := router.New()
			r.SetLogger(log.NewNullLogger())
			r.SetEventDispatcher(httpEvents.dispatch)
			r.Get("/work", func(c *router.Context) error {
				if tt.fail {
					return errors.New("boom")
				}
				return c.String(http.StatusOK, "ok")
			})
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/work", nil))
			if tt.fail && rec.Code != http.StatusInternalServerError {
				t.Fatalf("HTTP status = %d, want 500", rec.Code)
			}

			unaryEvents := &eventNameRecorder{}
			pair := interceptors.CallLifecycle(interceptors.WithEventDispatcher(unaryEvents.dispatch))
			_, _ = pair.Unary(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/svc.Work/Do"},
				func(context.Context, any) (any, error) {
					if tt.fail {
						return nil, status.Error(codes.Internal, "boom")
					}
					return "ok", nil
				})

			streamEvents := &eventNameRecorder{}
			pair = interceptors.CallLifecycle(interceptors.WithEventDispatcher(streamEvents.dispatch))
			_ = pair.Stream(nil, &contextServerStream{ctx: context.Background()}, &grpc.StreamServerInfo{FullMethod: "/svc.Work/Stream"},
				func(any, grpc.ServerStream) error {
					if tt.fail {
						return status.Error(codes.Internal, "boom")
					}
					return nil
				})

			for _, got := range []struct {
				name string
				rec  *eventNameRecorder
			}{{"http", httpEvents}, {"grpc unary", unaryEvents}, {"grpc stream", streamEvents}} {
				if verbs := got.rec.verbs(); !slices.Equal(verbs, tt.wantVerbs) {
					t.Errorf("%s lifecycle %v (%v), want %v", got.name, verbs, got.rec.names, tt.wantVerbs)
				}
			}

			// The terminal event carries the answer on both transports.
			for _, ev := range httpEvents.events {
				if done, ok := ev.(*router.RequestHandled); ok && tt.fail && done.StatusCode != http.StatusInternalServerError {
					t.Errorf("router.RequestHandled.StatusCode = %d, want 500", done.StatusCode)
				}
			}
			for _, ev := range unaryEvents.events {
				if done, ok := ev.(*grpcevents.RequestCompleted); ok && tt.fail && done.StatusCode != codes.Internal {
					t.Errorf("grpcevents.RequestCompleted.StatusCode = %v, want Internal", done.StatusCode)
				}
			}
		})
	}
}

// contextServerStream is a grpc.ServerStream carrying only a context.
type contextServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *contextServerStream) Context() context.Context { return s.ctx }
