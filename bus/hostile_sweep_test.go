package bus

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/hostile"
)

// isCreateUser reports whether cmd is the command the sweeps aim their
// hostile code at, so a probe's own dispatch (a deleteUser) does not run
// it.
func isCreateUser(cmd any) bool {
	_, ok := cmd.(createUser)
	return ok
}

// A Bus survives user code that panics, blocks or re-enters, wherever a
// dispatch calls it: the handler, a middleware stage, the event dispatcher
// or the logger of LoggingMiddleware. A panic in the handler or a stage
// fails that command only, as the recovered panic; one in the event
// dispatcher or the logger never fails the command. No lock is held while
// user code runs, so the bus's other entry points return while it blocks
// or calls back in.
func TestHostileUserCode_Bus(t *testing.T) {
	for _, site := range []string{"handler", "middleware", "events", "logger"} {
		for _, mode := range hostile.Modes() {
			t.Run(site+"/"+mode.String(), func(t *testing.T) {
				b := New()
				var ran, probed atomic.Int32
				probe := func() {
					Register(b, func(selfHandlingCmd) error { return nil })
					if site != "logger" {
						// A logger runs its code on every line, so only the
						// registration probes it.
						_ = b.Dispatch(deleteUser{ID: 1})
					}
					probed.Add(1)
				}
				c := hostile.New(t, mode, probe)
				Register(b, func(createUser) error {
					if site == "handler" {
						c.Run()
					}
					ran.Add(1)
					return nil
				})
				Register(b, func(deleteUser) error { return nil })
				switch site {
				case "middleware":
					b.Through(Middleware(func(cmd Command, next func(Command) error) error {
						if isCreateUser(cmd) {
							c.Run()
						}
						return next(cmd)
					}))
				case "events":
					b.SetEventDispatcher(func(_ context.Context, event any) error {
						if e, ok := event.(*CommandDispatching); ok && e.CommandType == "bus.createUser" {
							c.Run()
						}
						return nil
					})
				case "logger":
					b.Through(LoggingMiddleware(hostile.NewLogger(c, hostile.Info)))
				}

				var err error
				call := func() { err = b.Dispatch(createUser{Name: "a"}) }
				if mode == hostile.Block {
					done := make(chan any, 1)
					go func() { done <- hostile.Within(t, hostile.Deadline, call) }() //safe-goroutine: Within recovers and hands the panic back through done
					select {
					case <-c.Entered():
					case <-time.After(hostile.Deadline):
						t.Fatal("the user code never ran")
					}
					if p := hostile.Within(t, hostile.Deadline, probe); p != nil {
						t.Fatalf("probe panicked: %v", p)
					}
					c.Release()
					if p := <-done; p != nil {
						t.Fatalf("panic escaped Dispatch: %v", p)
					}
				} else if p := hostile.Within(t, hostile.Deadline, call); p != nil {
					t.Fatalf("panic escaped Dispatch: %v", p)
				}

				failsCommand := mode == hostile.Panic && (site == "handler" || site == "middleware")
				var rp contract.RecoveredPanic
				switch {
				case failsCommand && (!errors.As(err, &rp) || rp.Recovered() != hostile.PanicValue):
					t.Errorf("Dispatch = %v, want the recovered panic", err)
				case !failsCommand && err != nil:
					t.Errorf("Dispatch = %v, want nil", err)
				}
				wantRan := int32(1)
				if failsCommand {
					wantRan = 0
				}
				if got := ran.Load(); got != wantRan {
					t.Errorf("handler ran %d times, want %d", got, wantRan)
				}
				if mode != hostile.Panic && probed.Load() != 1 {
					t.Errorf("probe ran %d times, want 1", probed.Load())
				}
			})
		}
	}
}
