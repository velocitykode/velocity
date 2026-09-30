package grpc

import (
	"errors"
	"net"
	"net/http"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
)

// gatewayAcceptFails is a listener whose Accept returns err.
type gatewayAcceptFails struct {
	net.Listener
	err error
}

func (l gatewayAcceptFails) Accept() (net.Conn, error) { return nil, l.err }

// closedIsPanics is an error whose Is panics when asked whether it is
// http.ErrServerClosed.
type closedIsPanics struct{}

func (closedIsPanics) Error() string { return "accept failed" }
func (closedIsPanics) Is(target error) bool {
	if target == http.ErrServerClosed {
		panic("Is broke")
	}
	return false
}

// closedLoop unwraps to itself.
type closedLoop struct{}

func (e *closedLoop) Error() string { return "accept loop" }
func (e *closedLoop) Unwrap() error { return e }

// A gateway serve loop that ends with an error whose Is panics, or whose
// chain loops back on itself, still runs the stop that ends a failed
// serve: the check that tells a stop's own end from a failure is bounded
// and contained. serve is driven directly: the gateway binds its own
// listener, and net/http hands its Accept error back unchanged.
func TestGateway_FailedServeWithHostileErrorStops(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"Is panics", closedIsPanics{}},
		{"chain loops", &closedLoop{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			t.Cleanup(func() { _ = raw.Close() })
			g := NewGateway()
			c := &gatewayLife{
				run:       g.own.NewRun(),
				srv:       &http.Server{Handler: http.NotFoundHandler()},
				served:    true,
				started:   make(chan struct{}),
				serveDone: make(chan struct{}),
			}
			c.run.Admit()
			c.lis = newServeListener(gatewayAcceptFails{Listener: raw, err: tc.err}, func() {}, g.logLine, "HTTP gateway")
			var serveErr error
			if p := hostile.Within(t, hostile.Deadline, func() { serveErr = g.serve(c, false) }); p != nil {
				t.Fatalf("serve panicked: %v", p)
			}
			if !errors.Is(serveErr, tc.err) && serveErr != tc.err {
				t.Fatalf("serve = %v, want the Accept error", serveErr)
			}
			hostile.Eventually(t, hostile.Deadline, "the failed serve's stop finished", func() bool {
				select {
				case <-c.run.Finished():
					return true
				default:
					return false
				}
			})
		})
	}
}
