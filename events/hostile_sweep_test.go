package events

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/hostile"
)

// hostileListener runs its hostile code on every event it handles.
type hostileListener struct{ c *hostile.Code }

func (l hostileListener) Handle(context.Context, interface{}) error { l.c.Run(); return nil }
func (hostileListener) Async() bool                                 { return false }

// sweepTarget is one dispatch path under a sweep: listen registers a
// listener, deliver sends "evt" down the path, and probe calls the
// component's other entry points, which must return while a listener
// blocks or re-enters.
type sweepTarget struct {
	listen  func(event string, l Listener)
	deliver func(ctx context.Context) error
	probe   func()
	// stop tears the component down, when it has background work.
	stop func()
}

// defaultProbe calls the entry points every dispatcher shares.
func defaultProbe(d *DefaultDispatcher) func() {
	return func() {
		d.Listen("other", &BaseListener{})
		_ = d.Dispatch(context.Background(), "other")
		_ = d.HasListeners("evt")
		_ = d.GetListeners("evt")
	}
}

// syncTargets are the dispatch paths whose caller receives the delivery's
// result.
func syncTargets() map[string]func() sweepTarget {
	return map[string]func() sweepTarget{
		"Dispatch": func() sweepTarget {
			d := NewDispatcher()
			return sweepTarget{listen: func(e string, l Listener) { d.Listen(e, l) },
				deliver: func(ctx context.Context) error { return d.Dispatch(ctx, "evt") }, probe: defaultProbe(d)}
		},
		"DispatchNow": func() sweepTarget {
			d := NewDispatcher()
			return sweepTarget{listen: func(e string, l Listener) { d.Listen(e, l) },
				deliver: func(ctx context.Context) error { return d.DispatchNow(ctx, "evt") }, probe: defaultProbe(d)}
		},
		"QueueIntegrated": func() sweepTarget {
			d := NewQueueIntegratedDispatcher()
			return sweepTarget{listen: func(e string, l Listener) { d.Listen(e, l) },
				deliver: func(ctx context.Context) error { return d.Dispatch(ctx, "evt") }, probe: defaultProbe(d.DefaultDispatcher)}
		},
		"Stoppable": func() sweepTarget {
			d := NewStoppablePropagationDispatcher()
			return sweepTarget{listen: func(e string, l Listener) { d.Listen(e, l) },
				deliver: func(ctx context.Context) error { return d.Dispatch(ctx, "evt") }, probe: defaultProbe(d.DefaultDispatcher)}
		},
		"BatchingFlush": func() sweepTarget {
			d := NewBatchingDispatcher(10, time.Hour)
			return sweepTarget{listen: func(e string, l Listener) { d.Listen(e, l) },
				deliver: func(ctx context.Context) error {
					if err := d.Dispatch(ctx, "evt"); err != nil {
						return err
					}
					return d.Flush()
				},
				probe: func() {
					defaultProbe(d.DefaultDispatcher)()
					_ = d.Dispatch(context.Background(), "other")
					_ = d.GetBatchSize()
					_ = d.Flush()
				}}
		},
	}
}

// Every synchronous dispatch path survives a listener that panics, blocks
// or re-enters: a panic fails that listener only and reaches the caller as
// the recovered panic, the healthy listener still runs, and no lock is
// held while the listener runs, so the component's other entry points
// return while it blocks or calls back in.
func TestHostileListener_SyncDispatchPaths(t *testing.T) {
	for name, build := range syncTargets() {
		for _, mode := range hostile.Modes() {
			t.Run(name+"/"+mode.String(), func(t *testing.T) {
				target := build()
				c := hostile.New(t, mode, func() { target.probe() })
				var seen atomic.Int32
				target.listen("evt", hostileListener{c: c})
				target.listen("evt", tallyListener{n: &seen})
				ctx := context.Background()

				var err error
				if mode == hostile.Block {
					done := make(chan error, 1)
					go func() { done <- target.deliver(ctx) }() //safe-goroutine: the test reads the result or reports the hang
					select {
					case <-c.Entered():
					case <-time.After(hostile.Deadline):
						t.Fatal("the listener never ran")
					}
					if p := hostile.Within(t, hostile.Deadline, target.probe); p != nil {
						t.Fatalf("probe panicked: %v", p)
					}
					c.Release()
					select {
					case err = <-done:
					case <-time.After(hostile.Deadline):
						t.Fatal("the delivery did not return after Release")
					}
				} else if p := hostile.Within(t, hostile.Deadline, func() { err = target.deliver(ctx) }); p != nil {
					t.Fatalf("panic escaped the dispatch: %v", p)
				}

				var rp contract.RecoveredPanic
				switch {
				case mode == hostile.Panic && (!errors.As(err, &rp) || rp.Recovered() != hostile.PanicValue):
					t.Errorf("dispatch = %v, want the recovered panic", err)
				case mode != hostile.Panic && err != nil:
					t.Errorf("dispatch = %v, want nil", err)
				}
				if got := seen.Load(); got != 1 {
					t.Errorf("healthy listener handled %d events, want 1", got)
				}
			})
		}
	}
}

// detachedTargets are the dispatch paths whose delivery runs after the
// call returned, on a goroutine no caller waits on. Each wires its
// recorder with the given function.
func detachedTargets() map[string]func(record func(context.Context, error, any)) sweepTarget {
	listenOn := func(d *DefaultDispatcher) func(string, Listener) {
		return func(e string, l Listener) { d.Listen(e, l) }
	}
	return map[string]func(func(context.Context, error, any)) sweepTarget{
		"DispatchAfter": func(rec func(context.Context, error, any)) sweepTarget {
			d := NewDispatcher()
			d.SetDetachedFailureRecorder(rec)
			return sweepTarget{listen: listenOn(d), probe: defaultProbe(d),
				deliver: func(ctx context.Context) error { return d.DispatchAfter(ctx, "evt", time.Millisecond) }}
		},
		"DispatchAsync": func(rec func(context.Context, error, any)) sweepTarget {
			d := NewDispatcher()
			d.SetDetachedFailureRecorder(rec)
			return sweepTarget{listen: listenOn(d), probe: defaultProbe(d),
				deliver: func(ctx context.Context) error { return d.DispatchAsync(ctx, "evt") }}
		},
		"Debouncing": func(rec func(context.Context, error, any)) sweepTarget {
			d := NewDebouncingDispatcher(time.Millisecond)
			d.SetDetachedFailureRecorder(rec)
			return sweepTarget{listen: listenOn(d.DefaultDispatcher), stop: d.Stop,
				deliver: func(ctx context.Context) error { return d.Dispatch(ctx, "evt") },
				probe: func() {
					defaultProbe(d.DefaultDispatcher)()
					_ = d.Dispatch(context.Background(), "other")
					_ = d.GetPendingCount()
				}}
		},
		"Coalescing": func(rec func(context.Context, error, any)) sweepTarget {
			d := NewCoalescingDispatcher(time.Millisecond)
			d.SetDetachedFailureRecorder(rec)
			return sweepTarget{listen: listenOn(d.DefaultDispatcher), stop: d.Stop,
				deliver: func(ctx context.Context) error { return d.Dispatch(ctx, "evt") },
				probe: func() {
					defaultProbe(d.DefaultDispatcher)()
					_ = d.Dispatch(context.Background(), "other")
					_ = d.GetCoalescedCount("other")
				}}
		},
		"BatchingLoop": func(rec func(context.Context, error, any)) sweepTarget {
			d := NewBatchingDispatcher(10, time.Millisecond)
			d.SetDetachedFailureRecorder(rec)
			d.Start()
			return sweepTarget{listen: listenOn(d.DefaultDispatcher), stop: d.Stop,
				deliver: func(ctx context.Context) error { return d.Dispatch(ctx, "evt") },
				probe: func() {
					defaultProbe(d.DefaultDispatcher)()
					_ = d.Dispatch(context.Background(), "other")
					_ = d.GetBatchSize()
					_ = d.Flush()
				}}
		},
	}
}

// Every detached dispatch path survives a listener that panics, blocks or
// re-enters. A panic is contained on the delivering goroutine (the
// scenario runs in a child process, so an escaped panic fails it alone)
// and the delivery is recorded once; a listener that blocks or calls back
// in holds no lock of the component's, so its other entry points return;
// and the healthy listener still runs.
func TestHostileListener_DetachedDispatchPaths(t *testing.T) {
	for name, build := range detachedTargets() {
		for _, mode := range hostile.Modes() {
			t.Run(name+"/"+mode.String(), func(t *testing.T) {
				run := func() {
					var recorded, seen atomic.Int32
					target := build(func(context.Context, error, any) { recorded.Add(1) })
					if target.stop != nil {
						defer func() {
							if p := hostile.Within(t, hostile.Deadline, target.stop); p != nil {
								t.Errorf("Stop panicked: %v", p)
							}
						}()
					}
					c := hostile.New(t, mode, func() { target.probe() })
					target.listen("evt", hostileListener{c: c})
					target.listen("evt", tallyListener{n: &seen})
					if err := target.deliver(context.Background()); err != nil {
						t.Fatalf("dispatch = %v", err)
					}
					select {
					case <-c.Entered():
					case <-time.After(hostile.Deadline):
						t.Fatal("the listener never ran")
					}
					if mode == hostile.Block {
						if p := hostile.Within(t, hostile.Deadline, target.probe); p != nil {
							t.Fatalf("probe panicked: %v", p)
						}
						c.Release()
					}
					waitFor(func() bool { return seen.Load() == 1 })
					if mode == hostile.Panic {
						waitFor(func() bool { return recorded.Load() > 0 })
						// Give a second record, or an escaped panic, time to
						// show; a slow machine can only make this pass falsely.
						time.Sleep(20 * time.Millisecond)
					}
					if got := seen.Load(); got != 1 {
						t.Errorf("healthy listener handled %d events, want 1", got)
					}
					want := int32(0)
					if mode == hostile.Panic {
						want = 1
					}
					if got := recorded.Load(); got != want {
						t.Errorf("recorded %d failed deliveries, want %d", got, want)
					}
				}
				if mode == hostile.Panic {
					hostile.Isolated(t, run)
					return
				}
				run()
			})
		}
	}
}
