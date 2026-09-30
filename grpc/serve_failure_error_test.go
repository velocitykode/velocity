package grpc_test

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	grpcgo "google.golang.org/grpc"

	"github.com/velocitykode/velocity/grpc"
	"github.com/velocitykode/velocity/grpc/grpcevents"
	"github.com/velocitykode/velocity/internal/hostile"
)

// acceptFails is a listener whose Accept returns err.
type acceptFails struct {
	net.Listener
	err error
}

func (l acceptFails) Accept() (net.Conn, error) { return nil, l.err }

// stoppedIsPanics is an Accept error whose Is panics when asked whether it
// is grpc.ErrServerStopped.
type stoppedIsPanics struct{}

func (stoppedIsPanics) Error() string { return "accept failed" }
func (stoppedIsPanics) Is(target error) bool {
	if target == grpcgo.ErrServerStopped {
		panic("Is broke")
	}
	return false
}

// acceptLoop is an Accept error that unwraps to itself.
type acceptLoop struct{}

func (e *acceptLoop) Error() string { return "accept loop" }
func (e *acceptLoop) Unwrap() error { return e }

// A serve loop whose listener's Accept fails with an error whose Is panics,
// or whose chain loops back on itself, still stops the server: the check
// that tells a stop's own end from a failure is bounded and contained, so
// ServerStopped is dispatched and Start returns the error.
func TestServer_FailedServeWithHostileAcceptErrorStops(t *testing.T) {
	for _, start := range []string{"StartAsync", "Start"} {
		for _, tc := range []struct {
			name string
			err  error
		}{
			{"Is panics", stoppedIsPanics{}},
			{"chain loops", &acceptLoop{}},
		} {
			t.Run(start+"/"+tc.name, func(t *testing.T) {
				s := grpc.NewServer(grpc.WithListener(acceptFails{Listener: loopback(t), err: tc.err}))
				var stopped atomic.Int32
				s.SetEventDispatcher(func(_ context.Context, ev any) error {
					if _, ok := ev.(*grpcevents.ServerStopped); ok {
						stopped.Add(1)
					}
					return nil
				})
				stopOnCleanup(t, s)
				switch start {
				case "StartAsync":
					if err := s.StartAsync(); err != nil {
						t.Fatalf("StartAsync = %v, want nil (the serve loop fails later)", err)
					}
				case "Start":
					var err error
					if p := hostile.Within(t, hostile.Deadline, func() { err = s.Start() }); p != nil {
						t.Fatalf("Start panicked: %v", p)
					}
					if err != tc.err {
						t.Fatalf("Start = %v, want the Accept error", err)
					}
				}
				hostile.Eventually(t, hostile.Deadline, "ServerStopped", func() bool { return stopped.Load() == 1 })
				if s.IsRunning() {
					t.Error("the server still reports running")
				}
			})
		}
	}
}
