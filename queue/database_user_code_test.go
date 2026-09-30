package queue

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// Jobs whose registered factory runs a test's hostile code. Each test uses
// its own job type, so no two tests share a registry entry.

type factoryJobA struct{ ID string }
type factoryJobB struct{ ID string }
type factoryJobC struct{ ID string }
type factoryJobD struct{ ID string }
type factoryJobE struct{ ID string }

func (*factoryJobA) Handle() error { return nil }
func (*factoryJobA) Failed(error)  {}
func (*factoryJobB) Handle() error { return nil }
func (*factoryJobB) Failed(error)  {}
func (*factoryJobC) Handle() error { return nil }
func (*factoryJobC) Failed(error)  {}
func (*factoryJobD) Handle() error { return nil }
func (*factoryJobD) Failed(error)  {}
func (*factoryJobE) Handle() error { return nil }
func (*factoryJobE) Failed(error)  {}

// hostileError is an error whose text is user code.
type hostileError struct{ code *hostile.Code }

func (e hostileError) Error() string {
	e.code.Run()
	return "hostile failure"
}

// TestDatabaseDriver_Pop_FactoryReentersDriver runs the job factory as
// user code that calls back into the driver (Clear, Size, a nested pop)
// in both pop modes. The factory runs after the pop released the worker
// lock and committed its transaction, so every call returns.
func TestDatabaseDriver_Pop_FactoryReentersDriver(t *testing.T) {
	for _, mode := range []popMode{popModeReserve, popModeDelete} {
		name := map[popMode]string{popModeReserve: "reserve", popModeDelete: "delete"}[mode]
		t.Run(name, func(t *testing.T) {
			d, cleanup := newSQLiteQueueDB(t)
			defer cleanup()
			ctx := context.Background()
			var reentry []error
			code := hostile.New(t, hostile.Reenter, func() {
				_, err := d.Size("other")
				reentry = append(reentry, err)
				job, _, _, err := d.popSelect(ctx, "default", mode)
				if job != nil {
					err = errors.New("nested pop returned the row being popped")
				}
				reentry = append(reentry, err)
				reentry = append(reentry, d.Clear("other"))
			})
			Register("factoryJobA", func(data []byte) (Job, error) {
				code.Run()
				return &factoryJobA{}, nil
			})
			if err := d.PushCtx(ctx, &factoryJobA{ID: "1"}, "default"); err != nil {
				t.Fatalf("PushCtx: %v", err)
			}
			var job Job
			var err error
			hostile.Within(t, hostile.Deadline, func() {
				job, _, _, err = d.popSelect(ctx, "default", mode)
			})
			if code.Calls() == 0 {
				t.Fatal("the factory never ran")
			}
			if err != nil || job == nil {
				t.Fatalf("pop = %v, %v; want the job", job, err)
			}
			for i, e := range reentry {
				if e != nil {
					t.Errorf("re-entered call %d: %v", i, e)
				}
			}
			n, err := d.Size("default")
			if err != nil || n != 0 {
				t.Fatalf("Size after pop = %d, %v; want 0 (row reserved or removed)", n, err)
			}
		})
	}
}

// TestDatabaseDriver_Pop_FactoryClearsItsOwnRow clears the queue from the
// factory: the popped row is gone before the pop finishes. A reserve-mode
// pop still hands out the job, whose reservation Ack then reports lost; a
// delete-mode pop reports the lease lost and delivers nothing.
func TestDatabaseDriver_Pop_FactoryClearsItsOwnRow(t *testing.T) {
	d, cleanup := newSQLiteQueueDB(t)
	defer cleanup()
	ctx := context.Background()
	code := hostile.New(t, hostile.Reenter, func() { _ = d.Clear("default") })
	Register("factoryJobB", func(data []byte) (Job, error) {
		code.Run()
		return &factoryJobB{}, nil
	})
	if err := d.PushCtx(ctx, &factoryJobB{ID: "1"}, "default"); err != nil {
		t.Fatalf("PushCtx: %v", err)
	}
	var job Job
	var err error
	hostile.Within(t, hostile.Deadline, func() { job, err = d.PopCtx(ctx, "default") })
	if !errors.Is(err, ErrLeaseLost) || job != nil {
		t.Fatalf("PopCtx = %v, %v; want ErrLeaseLost and no job", job, err)
	}
}

// TestDatabaseDriver_Pop_HostileFactory sweeps the factory through every
// kind of hostile user code. A blocked factory holds up no other call on
// the driver; a panicking one makes the row poison: it is quarantined with
// a fixed text, the pop returns ErrPoisonJob carrying the recovered value,
// and the driver stays usable.
func TestDatabaseDriver_Pop_HostileFactory(t *testing.T) {
	for _, mode := range hostile.Modes() {
		t.Run(mode.String(), func(t *testing.T) {
			d, cleanup := newSQLiteQueueDB(t)
			defer cleanup()
			ctx := context.Background()
			var reentered error
			code := hostile.New(t, mode, func() { reentered = d.Clear("other") })
			Register("factoryJobC", func(data []byte) (Job, error) {
				code.Run()
				return &factoryJobC{}, nil
			})
			if err := d.PushCtx(ctx, &factoryJobC{ID: "1"}, "default"); err != nil {
				t.Fatalf("PushCtx: %v", err)
			}
			type result struct {
				job      Job
				err      error
				panicked any
			}
			done := make(chan result, 1)
			go func() { //safe-goroutine: the pop under test; its result and panic are read below
				var r result
				defer func() {
					r.panicked = recover()
					done <- r
				}()
				r.job, _, _, r.err = d.PopCtxReserved(ctx, "default")
			}()
			if mode == hostile.Block {
				if !code.AwaitEntered(t) {
					return
				}
				hostile.Within(t, hostile.Deadline, func() {
					if err := d.PushCtx(ctx, &TestJob{ID: "side"}, "side"); err != nil {
						t.Errorf("PushCtx while a factory is blocked: %v", err)
					}
					if _, err := d.Size("side"); err != nil {
						t.Errorf("Size while a factory is blocked: %v", err)
					}
					if err := d.Clear("side"); err != nil {
						t.Errorf("Clear while a factory is blocked: %v", err)
					}
					if job, _, _, err := d.PopCtxReserved(ctx, "default"); job != nil || err != nil {
						t.Errorf("second pop while a factory is blocked = %v, %v; want nothing", job, err)
					}
				})
				code.Release()
			}
			var r result
			hostile.Within(t, hostile.Deadline, func() { r = <-done })
			if code.Calls() == 0 {
				t.Fatal("the factory never ran")
			}
			switch mode {
			case hostile.Panic:
				if r.panicked != nil || r.job != nil || !errors.Is(r.err, ErrPoisonJob) {
					t.Fatalf("pop = %v, %v (panicked: %v); want ErrPoisonJob and no panic", r.job, r.err, r.panicked)
				}
				if pe := panicerr.AsTyped(r.err); pe == nil || pe.Recovered() != hostile.PanicValue {
					t.Fatalf("pop error %v does not carry the recovered value", r.err)
				}
				assertFailedJobs(t, d, 1, errHydrationPanicked)
				if job, _, _, err := d.PopCtxReserved(ctx, "default"); job != nil || err != nil {
					t.Fatalf("pop after quarantine = %v, %v; want nothing", job, err)
				}
			default:
				if r.err != nil || r.job == nil || reentered != nil {
					t.Fatalf("pop = %v, %v (re-entered: %v); want the job", r.job, r.err, reentered)
				}
			}
			// The driver still pops and clears.
			hostile.Within(t, hostile.Deadline, func() {
				if err := d.Clear("default"); err != nil {
					t.Errorf("Clear after the factory: %v", err)
				}
			})
		})
	}
}

// TestDatabaseDriver_Pop_PoisonErrorTextPanics quarantines a job whose
// factory fails with an error whose Error method panics: the pop returns
// ErrPoisonJob instead of unwinding, and the row is recorded once.
func TestDatabaseDriver_Pop_PoisonErrorTextPanics(t *testing.T) {
	d, cleanup := newSQLiteQueueDB(t)
	defer cleanup()
	ctx := context.Background()
	code := hostile.New(t, hostile.Panic, nil)
	Register("factoryJobE", func(data []byte) (Job, error) {
		return nil, hostileError{code}
	})
	if err := d.PushCtx(ctx, &factoryJobE{ID: "1"}, "default"); err != nil {
		t.Fatalf("PushCtx: %v", err)
	}
	var err error
	if p := hostile.Within(t, hostile.Deadline, func() { _, _, _, err = d.PopCtxReserved(ctx, "default") }); p != nil {
		t.Fatalf("pop panicked: %v", p)
	}
	if code.Calls() == 0 {
		t.Fatal("the error text was never read")
	}
	if !errors.Is(err, ErrPoisonJob) {
		t.Fatalf("pop = %v; want ErrPoisonJob", err)
	}
	assertFailedJobs(t, d, 1, "velocity/queue: failed to restore job from wrapper")
}

// TestDatabaseDriver_Pop_PoisonErrorTextRunsOutsideTheLock quarantines a
// job whose factory fails with an error whose Error method calls back into
// the driver: the text is read before the quarantine takes the worker
// lock.
func TestDatabaseDriver_Pop_PoisonErrorTextRunsOutsideTheLock(t *testing.T) {
	d, cleanup := newSQLiteQueueDB(t)
	defer cleanup()
	ctx := context.Background()
	var reentered error
	code := hostile.New(t, hostile.Reenter, func() { reentered = d.Clear("other") })
	Register("factoryJobD", func(data []byte) (Job, error) {
		return nil, hostileError{code}
	})
	if err := d.PushCtx(ctx, &factoryJobD{ID: "1"}, "default"); err != nil {
		t.Fatalf("PushCtx: %v", err)
	}
	var err error
	hostile.Within(t, hostile.Deadline, func() { _, _, _, err = d.PopCtxReserved(ctx, "default") })
	if code.Calls() == 0 {
		t.Fatal("the error text was never read")
	}
	if !errors.Is(err, ErrPoisonJob) || reentered != nil {
		t.Fatalf("pop = %v (re-entered: %v); want ErrPoisonJob", err, reentered)
	}
	assertFailedJobs(t, d, 1, "hostile failure")
	if n, _ := d.Size("default"); n != 0 {
		t.Fatalf("Size after quarantine = %d, want 0", n)
	}
}

func assertFailedJobs(t *testing.T, d *DatabaseDriver, want int, exceptionContains string) {
	t.Helper()
	rows, err := d.db.Query("SELECT exception FROM failed_jobs")
	if err != nil {
		t.Fatalf("query failed_jobs: %v", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var exc string
		if err := rows.Scan(&exc); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if !strings.Contains(exc, exceptionContains) {
			t.Errorf("failed_jobs exception = %q, want it to contain %q", exc, exceptionContains)
		}
		n++
	}
	if n != want {
		t.Fatalf("failed_jobs rows = %d, want %d", n, want)
	}
}

// TestDatabaseDriver_PopAgainstClear_Stress races pops, pushes and clears
// on one driver: none of them may deadlock, and every popped job is
// delivered once.
func TestDatabaseDriver_PopAgainstClear_Stress(t *testing.T) {
	d, cleanup := newSQLiteQueueDB(t)
	defer cleanup()
	ctx := context.Background()
	var wg sync.WaitGroup
	seen := sync.Map{}
	var dup error
	var dupOnce sync.Once
	hostile.Within(t, 4*hostile.Deadline, func() {
		for w := 0; w < 4; w++ {
			wg.Add(3)
			go func() { //safe-goroutine: stress pusher, joined by wg
				defer wg.Done()
				for i := 0; i < 25; i++ {
					_ = d.PushCtx(ctx, &TestJob{ID: "x"}, "default")
				}
			}()
			go func() { //safe-goroutine: stress popper, joined by wg
				defer wg.Done()
				for i := 0; i < 25; i++ {
					_, token, _, err := d.PopCtxReserved(ctx, "default")
					if err == nil && !token.IsZero() {
						if _, loaded := seen.LoadOrStore(token.ID, true); loaded {
							dupOnce.Do(func() { dup = errors.New("a row was delivered twice") })
						}
					}
				}
			}()
			go func() { //safe-goroutine: stress clearer, joined by wg
				defer wg.Done()
				for i := 0; i < 5; i++ {
					_ = d.Clear("default")
				}
			}()
		}
		wg.Wait()
	})
	if dup != nil {
		t.Fatal(dup)
	}
}

// BenchmarkDatabaseDriver_Pop pushes one job and pops it, reserved then
// acked (the worker path) or removed by PopCtx.
func BenchmarkDatabaseDriver_Pop(b *testing.B) {
	for _, reserved := range []bool{true, false} {
		name := map[bool]string{true: "reserved", false: "delete"}[reserved]
		b.Run(name, func(b *testing.B) {
			d, cleanup := newSQLiteQueueDB(b)
			b.Cleanup(cleanup)
			ctx := context.Background()
			b.ReportAllocs()
			for b.Loop() {
				if err := d.PushCtx(ctx, &TestJob{ID: "1"}, "default"); err != nil {
					b.Fatal(err)
				}
				if reserved {
					job, token, _, err := d.PopCtxReserved(ctx, "default")
					if err != nil || job == nil {
						b.Fatal(job, err)
					}
					if err := d.AckCtx(ctx, token); err != nil {
						b.Fatal(err)
					}
				} else if job, err := d.PopCtx(ctx, "default"); err != nil || job == nil {
					b.Fatal(job, err)
				}
			}
		})
	}
}
