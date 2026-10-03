package teardown_test

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/internal/teardown"
)

// A Shutdown called from a child's close is refused before anything is
// detached: the registry keeps what was published meanwhile and the
// generation stays; the next Shutdown closes it.
func TestChildren_NestedShutdownLeavesTheRegistryIntact(t *testing.T) {
	var m manager
	var nested error
	var heldInside int
	var genInside uint64
	late := &child{}
	code := hostile.New(t, hostile.Reenter, func() {
		m.add("late", late)
		nested = m.Shutdown(context.Background())
		heldInside = m.len()
		genInside = m.generation()
	})
	m.add("reenters", &child{code: code})
	hostile.Within(t, hostile.Deadline, func() { _ = m.Shutdown(context.Background()) })
	if !errors.Is(nested, contract.ErrStopFromOwnWork) {
		t.Errorf("nested Shutdown = %v, want ErrStopFromOwnWork", nested)
	}
	if heldInside != 1 {
		t.Errorf("registry after the nested Shutdown holds %d children, want 1 (the late one)", heldInside)
	}
	if genInside != 1 {
		t.Errorf("generation after the nested Shutdown = %d, want 1 (only the outer Shutdown counts)", genInside)
	}
	if n := late.calls.Load(); n != 0 {
		t.Fatalf("late child closed %d times by the refused Shutdown, want 0", n)
	}
	hostile.Within(t, hostile.Deadline, func() { _ = m.Shutdown(context.Background()) })
	if n := late.calls.Load(); n != 1 {
		t.Errorf("late child closed %d times after the next Shutdown, want 1", n)
	}
}

// Every accepted Shutdown advances the generation, an empty registry's
// included.
func TestChildren_GenerationAdvancesOnEveryShutdown(t *testing.T) {
	var m manager
	if g := m.generation(); g != 0 {
		t.Fatalf("generation of a new manager = %d, want 0", g)
	}
	_ = m.Shutdown(context.Background())
	m.add("a", &child{})
	_ = m.Shutdown(context.Background())
	_ = m.Shutdown(context.Background())
	if g := m.generation(); g != 3 {
		t.Errorf("generation after three Shutdowns = %d, want 3", g)
	}
}

// A child whose close returns at the run's ctx, its work still going,
// keeps the run open: the run's result is the child's own once the work
// finished, never the ctx's error.
func TestChildren_AChildReturningAtItsDeadlineKeepsTheRunOpen(t *testing.T) {
	errWork := errors.New("work failed")
	d := &deadlineChild{code: hostile.New(t, hostile.Block, nil), err: errWork}
	var dm deadlineManager
	dm.children = map[string]*deadlineChild{"d": d}
	if err := dm.Shutdown(cancelled()); !errors.Is(err, context.Canceled) {
		t.Errorf("Shutdown with a done ctx = %v, want its ctx error", err)
	}
	if !d.code.AwaitEntered(t) {
		return
	}
	d.code.Release()
	var err error
	hostile.Within(t, hostile.Deadline, func() { err = dm.Shutdown(context.Background()) })
	if !errors.Is(err, errWork) {
		t.Errorf("later Shutdown = %v, want the child's own result (the run waited for its work)", err)
	}
	if n := d.works.Load(); n != 1 {
		t.Errorf("child work ran %d times, want 1", n)
	}
}

// teardownChildren is the Children of deadline children.
type teardownChildren = teardown.Children[*deadlineChild]

// deadlineManager is a manager of deadline children.
type deadlineManager struct {
	mu       sync.Mutex
	children map[string]*deadlineChild
	shutdown teardownChildren
}

func (m *deadlineManager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	wait := m.shutdown.Shutdown(&m.children, func(_ string, err error) error { return err })
	m.mu.Unlock()
	return wait(ctx)
}

// deadlineChild's first Shutdown starts its work (code, then err); every
// Shutdown waits for the work or its ctx and returns ctx's error at ctx,
// the work's result once it finished.
type deadlineChild struct {
	code  *hostile.Code
	err   error
	works atomic.Int32
	once  sync.Once
	done  chan struct{}
}

func (d *deadlineChild) Shutdown(ctx context.Context) error {
	d.once.Do(func() {
		d.done = make(chan struct{})
		go func() {
			d.works.Add(1)
			d.code.Run()
			close(d.done)
		}()
	})
	select {
	case <-d.done:
		return d.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// A replaced child is closed once, contained, by the call that replaced
// it, which gets its error; the next Shutdown does not report it again.
func TestChildren_ReplaceRetiresTheDisplacedChild(t *testing.T) {
	var m manager
	errOld := errors.New("old failed")
	old, next := &child{err: errOld}, &child{}
	_ = m.set("a", old)
	if err := m.set("a", next); !errors.Is(err, errOld) {
		t.Errorf("replacing set = %v, want the displaced child's close error", err)
	}
	if n := old.calls.Load(); n != 1 {
		t.Errorf("displaced child closed %d times, want 1", n)
	}
	if err := m.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown = %v, want nil (the retirement's error was the replacing call's)", err)
	}
	if n, m := old.calls.Load(), next.calls.Load(); n != 1 || m != 1 {
		t.Errorf("close calls displaced %d, current %d; want 1 each", n, m)
	}
}

// A panicking displaced child is contained and is the replacing call's
// error.
func TestChildren_RetireContainsAPanic(t *testing.T) {
	var m manager
	_ = m.set("a", &child{code: hostile.New(t, hostile.Panic, nil)})
	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("set panicked: %v", p)
			}
		}()
		err = m.set("a", &child{})
	}()
	if err == nil {
		t.Fatal("set = nil, want the displaced child's panic as its error")
	}
}

// One instance under two names is closed once: a replace while another
// name holds it closes nothing, the clear that releases it closes it
// once, and a Shutdown closes it once.
func TestChildren_OneInstanceUnderTwoNamesClosesOnce(t *testing.T) {
	var m manager
	x, y := &child{}, &child{}
	_ = m.set("a", x)
	_ = m.set("b", x)
	_ = m.set("a", y)
	if n := x.calls.Load(); n != 0 {
		t.Fatalf("child still held under b closed %d times, want 0", n)
	}
	_ = m.set("a", x)
	if err := m.clear(); err != nil {
		t.Fatalf("clear = %v", err)
	}
	if nx, ny := x.calls.Load(), y.calls.Load(); nx != 1 || ny != 1 {
		t.Errorf("clear closed x %d times, y %d times; want 1 each", nx, ny)
	}
	z := &child{}
	m.add("c", z)
	m.add("d", z)
	_ = m.Shutdown(context.Background())
	if n := z.calls.Load(); n != 1 {
		t.Errorf("Shutdown closed a child under two names %d times, want 1", n)
	}
}

// Re-assigning the same instance retires nothing; a nil or typed-nil
// displaced child is skipped; a retirement's close runs once however
// often it is called.
func TestChildren_RetireSkipsWhatItMustNotClose(t *testing.T) {
	var m manager
	x := &child{}
	_ = m.set("a", x)
	_ = m.set("a", x)
	if n := x.calls.Load(); n != 0 {
		t.Errorf("same instance re-assigned: closed %d times, want 0", n)
	}
	m.add("nil", nil)
	if err := m.set("nil", &child{}); err != nil {
		t.Errorf("replacing a typed nil = %v, want nil", err)
	}
	y := &child{}
	m.mu.Lock()
	m.children["y"] = y
	delete(m.children, "y")
	retire := m.shutdown.Retire(m.children, y)
	m.mu.Unlock()
	_ = retire()
	_ = retire()
	if n := y.calls.Load(); n != 1 {
		t.Errorf("a retirement close run twice closed %d times, want 1", n)
	}
	var none teardownChildren
	if err := none.Retire(nil, nil)(); err != nil {
		t.Errorf("Retire(nil) on a zero Children = %v, want nil", err)
	}
}

// A Shutdown waits for a retirement still closing: once its run has
// closed the registry's children, it stays unfinished (a Shutdown with a
// done ctx keeps answering that ctx's error, never the run's nil) until
// the retirement's close returns.
func TestChildren_ShutdownAwaitsASlowRetirement(t *testing.T) {
	var m manager
	slowCode := hostile.New(t, hostile.Block, nil)
	slow := &child{code: slowCode}
	_ = m.set("a", slow)
	currentCode := hostile.New(t, hostile.Block, nil)
	current := &child{code: currentCode}
	replaced := make(chan error, 1)
	go func() { replaced <- m.set("a", current) }()
	if !slowCode.AwaitEntered(t) {
		return
	}
	result := make(chan error, 1)
	go func() { result <- m.Shutdown(context.Background()) }()
	if !currentCode.AwaitEntered(t) {
		return
	}
	currentCode.Release()
	// The run has closed every child it detached; only the retirement
	// holds it. Each look yields: a run that did not wait would finish
	// within a few.
	for range 1000 {
		if err := m.Shutdown(cancelled()); !errors.Is(err, context.Canceled) {
			t.Fatalf("Shutdown while a retirement closes = %v, want its ctx error (the run waits for the retirement)", err)
		}
		runtime.Gosched()
	}
	slowCode.Release()
	var err error
	hostile.Within(t, hostile.Deadline, func() {
		<-replaced
		err = <-result
	})
	if err != nil {
		t.Errorf("Shutdown after the retirement finished = %v, want nil", err)
	}
	if n, c := slow.calls.Load(), current.calls.Load(); n != 1 || c != 1 {
		t.Errorf("close calls slow %d, current %d; want 1 each", n, c)
	}
}

// A Shutdown called from a retirement's close is nested: refused, the
// registry intact; OwnsCaller answers true there and false outside.
func TestChildren_ShutdownFromARetirementIsRefused(t *testing.T) {
	var m manager
	var nested error
	var owns bool
	code := hostile.New(t, hostile.Reenter, func() {
		owns = m.shutdown.OwnsCaller()
		nested = m.Shutdown(context.Background())
	})
	_ = m.set("a", &child{code: code})
	hostile.Within(t, hostile.Deadline, func() { _ = m.set("a", &child{}) })
	if !errors.Is(nested, contract.ErrStopFromOwnWork) {
		t.Errorf("Shutdown from a retirement = %v, want ErrStopFromOwnWork", nested)
	}
	if !owns {
		t.Error("OwnsCaller inside a retirement's close = false, want true")
	}
	if m.shutdown.OwnsCaller() {
		t.Error("OwnsCaller outside = true, want false")
	}
	if n := m.len(); n != 1 {
		t.Errorf("registry holds %d children after the refused Shutdown, want 1", n)
	}
}

// Replaces, clears and Shutdowns racing: every child is closed exactly
// once (run under -race).
func TestChildren_RetireConcurrentWithShutdown(t *testing.T) {
	var m manager
	var mu sync.Mutex
	var all []*child
	newChild := func() *child {
		c := &child{}
		mu.Lock()
		all = append(all, c)
		mu.Unlock()
		return c
	}
	var wg sync.WaitGroup
	for g := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 100 {
				_ = m.set(strconv.Itoa(i%5), newChild())
				if i%17 == g {
					_ = m.clear()
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 50 {
			_ = m.Shutdown(context.Background())
		}
	}()
	wg.Wait()
	_ = m.Shutdown(context.Background())
	for i, c := range all {
		if n := c.calls.Load(); n != 1 {
			t.Errorf("child %d closed %d times, want 1", i, n)
		}
	}
}

// Closing returns the children a run under way detached, and the ones a
// retirement is closing, until their closes returned: the registry no
// longer holds them, yet the manager's Shutdown still waits for them.
func TestChildren_ClosingHoldsTheChildrenOfARunUnderWay(t *testing.T) {
	var m manager
	if got := m.shutdown.Closing(); len(got) != 0 {
		t.Fatalf("Closing on a manager that never held a child = %v, want none", got)
	}
	retiredCode := hostile.New(t, hostile.Block, nil)
	retired := &child{code: retiredCode}
	_ = m.set("a", retired)
	if got := m.shutdown.Closing(); len(got) != 0 {
		t.Fatalf("Closing while every child is in the registry = %v, want none", got)
	}
	slowCode := hostile.New(t, hostile.Block, nil)
	slow := &child{code: slowCode}
	replaced := make(chan error, 1)
	go func() { replaced <- m.set("a", slow) }()
	if !retiredCode.AwaitEntered(t) {
		return
	}
	if got := m.shutdown.Closing(); len(got) != 1 || got[0] != retired {
		t.Fatalf("Closing while a retirement closes = %v, want the retired child", got)
	}
	result := make(chan error, 1)
	go func() { result <- m.Shutdown(context.Background()) }()
	if !slowCode.AwaitEntered(t) {
		return
	}
	if n := m.len(); n != 0 {
		t.Fatalf("registry holds %d children during the run, want 0", n)
	}
	got := m.shutdown.Closing()
	if len(got) != 2 || !slices.Contains(got, slow) || !slices.Contains(got, retired) {
		t.Fatalf("Closing during the run = %v, want the detached child and the retired one", got)
	}
	retiredCode.Release()
	slowCode.Release()
	hostile.Within(t, hostile.Deadline, func() {
		<-replaced
		if err := <-result; err != nil {
			t.Errorf("Shutdown = %v, want nil", err)
		}
	})
	if got := m.shutdown.Closing(); len(got) != 0 {
		t.Fatalf("Closing after the run finished = %v, want none", got)
	}
}

// Closing is read while runs begin and finish.
func TestChildren_ClosingConcurrentWithShutdown(t *testing.T) {
	var m manager
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := range 50 {
				_ = m.set("c"+strconv.Itoa(i), &child{})
				if j%5 == 0 {
					_ = m.Shutdown(context.Background())
				}
			}
		}()
		go func() {
			defer wg.Done()
			for range 200 {
				_ = m.shutdown.Closing()
			}
		}()
	}
	hostile.Within(t, hostile.Deadline, wg.Wait)
	hostile.Within(t, hostile.Deadline, func() { _ = m.Shutdown(context.Background()) })
	if got := m.shutdown.Closing(); len(got) != 0 {
		t.Fatalf("Closing after the last Shutdown = %v, want none", got)
	}
}
