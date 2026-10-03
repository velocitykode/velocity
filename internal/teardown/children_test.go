package teardown_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/internal/panicerr"
	"github.com/velocitykode/velocity/internal/teardown"
)

// manager is the shape a framework manager has: a registry under a lock,
// and a Shutdown through Children.
type manager struct {
	mu       sync.Mutex
	children map[string]*child
	shutdown teardown.Children[*child]
}

func (m *manager) add(name string, c *child) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.children == nil {
		m.children = map[string]*child{}
	}
	m.children[name] = c
}

func (m *manager) remove(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.children, name)
}

func (m *manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	wait := m.shutdown.Shutdown(&m.children, func(name string, err error) error { return fmt.Errorf("child %q: %w", name, err) })
	m.mu.Unlock()
	return wait(ctx)
}

// set publishes c under name and retires the child it displaces.
func (m *manager) set(name string, c *child) error {
	m.mu.Lock()
	if m.children == nil {
		m.children = map[string]*child{}
	}
	old, had := m.children[name]
	m.children[name] = c
	retire := noRetire
	if had {
		retire = m.shutdown.Retire(m.children, old)
	}
	m.mu.Unlock()
	return retire()
}

// clear empties the registry and retires every child it held.
func (m *manager) clear() error {
	m.mu.Lock()
	held := m.children
	m.children = map[string]*child{}
	var retires []func() error
	for _, c := range held {
		retires = append(retires, m.shutdown.Retire(m.children, c))
	}
	m.mu.Unlock()
	var errs []error
	for _, r := range retires {
		errs = append(errs, r())
	}
	return errors.Join(errs...)
}

func noRetire() error { return nil }

// generation reads the shutdown generation under the lock.
func (m *manager) generation() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.shutdown.Generation()
}

// len reports how many children the registry holds.
func (m *manager) len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.children)
}

// child's Shutdown runs code (panic, block, re-enter), then returns err.
type child struct {
	code  *hostile.Code
	err   error
	calls atomic.Int32
}

func (c *child) Shutdown(context.Context) error {
	c.calls.Add(1)
	c.code.Run()
	return c.err
}

func cancelled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// A manager that never held a child shuts down with nil.
func TestChildren_NothingPublished(t *testing.T) {
	var m manager
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown = %v, want nil", err)
	}
	if err := m.Shutdown(cancelled()); err != nil {
		t.Fatalf("Shutdown with a done ctx = %v, want nil", err)
	}
}

// Every child is shut down once, contained, with its name on its error.
func TestChildren_ShutsDownEveryChildContained(t *testing.T) {
	var m manager
	errA := errors.New("a failed")
	a, bad, ok := &child{err: errA}, &child{code: hostile.New(t, hostile.Panic, nil)}, &child{}
	m.add("a", a)
	m.add("bad", bad)
	m.add("ok", ok)
	err := m.Shutdown(context.Background())
	if !errors.Is(err, errA) || panicerr.AsTyped(err) == nil {
		t.Fatalf("Shutdown = %v, want a's error and bad's panic", err)
	}
	if want := `child "a": a failed`; !containsLine(err, want) {
		t.Errorf("Shutdown = %v, want %q among its errors", err, want)
	}
	for name, c := range map[string]*child{"a": a, "bad": bad, "ok": ok} {
		if n := c.calls.Load(); n != 1 {
			t.Errorf("%s Shutdown calls = %d, want 1", name, n)
		}
	}
}

func containsLine(err error, line string) bool {
	for _, e := range err.(interface{ Unwrap() []error }).Unwrap() {
		if e.Error() == line {
			return true
		}
	}
	return false
}

// An overlapping Shutdown waits for the run (here: its own ctx), and every
// later one with nothing new returns the run's retained result, also after
// a child was published and removed again.
func TestChildren_OverlapAwaitsAndTheResultIsRetained(t *testing.T) {
	var m manager
	errChild := errors.New("child failed")
	code := hostile.New(t, hostile.Block, nil)
	slow := &child{code: code, err: errChild}
	m.add("slow", slow)
	first := make(chan error, 1)
	go func() { first <- m.Shutdown(context.Background()) }()
	if !code.AwaitEntered(t) {
		return
	}
	if err := m.Shutdown(cancelled()); !errors.Is(err, context.Canceled) {
		t.Errorf("overlapping Shutdown = %v, want its ctx error", err)
	}
	code.Release()
	var err error
	hostile.Within(t, hostile.Deadline, func() { err = <-first })
	if !errors.Is(err, errChild) {
		t.Fatalf("first Shutdown = %v, want the child's error", err)
	}
	if err := m.Shutdown(context.Background()); !errors.Is(err, errChild) {
		t.Errorf("repeated Shutdown = %v, want the retained result", err)
	}
	m.add("gone", &child{})
	m.remove("gone")
	if err := m.Shutdown(context.Background()); !errors.Is(err, errChild) {
		t.Errorf("Shutdown after a publish and a removal = %v, want the retained result", err)
	}
	if n := slow.calls.Load(); n != 1 {
		t.Errorf("child Shutdown calls = %d, want 1", n)
	}
}

// A run begun while the one before it still shuts down waits for it and
// joins its result: the later Shutdown covers every child published
// before it.
func TestChildren_ALaterRunWaitsForTheEarlierOne(t *testing.T) {
	var m manager
	errSlow := errors.New("slow failed")
	code := hostile.New(t, hostile.Block, nil)
	m.add("slow", &child{code: code, err: errSlow})
	first := make(chan error, 1)
	go func() { first <- m.Shutdown(context.Background()) }()
	if !code.AwaitEntered(t) {
		return
	}
	later := &child{}
	m.add("later", later)
	if err := m.Shutdown(cancelled()); !errors.Is(err, context.Canceled) {
		t.Errorf("Shutdown of the later run while the earlier one runs = %v, want its ctx error", err)
	}
	code.Release()
	var err error
	hostile.Within(t, hostile.Deadline, func() {
		<-first
		err = m.Shutdown(context.Background())
	})
	if !errors.Is(err, errSlow) {
		t.Errorf("later run's result = %v, want the earlier run's error joined", err)
	}
	if n := later.calls.Load(); n != 1 {
		t.Errorf("later child Shutdown calls = %d, want 1", n)
	}
	// A run begun after the one before it finished does not carry its
	// result: that run's callers had it already.
	m.add("next", &child{})
	if err := m.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown of a run begun after the others finished = %v, want nil", err)
	}
}

// A Shutdown from a child's Shutdown is refused with ErrStopFromOwnWork,
// and a child that publishes during the run does not deadlock: the next
// Shutdown shuts the new child down.
func TestChildren_ReentryFromAChild(t *testing.T) {
	var m manager
	var nested error
	late := &child{}
	code := hostile.New(t, hostile.Reenter, func() {
		m.add("late", late)
		nested = m.Shutdown(context.Background())
	})
	m.add("reenters", &child{code: code})
	hostile.Within(t, hostile.Deadline, func() {
		if err := m.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown = %v, want nil", err)
		}
	})
	if !errors.Is(nested, contract.ErrStopFromOwnWork) {
		t.Errorf("Shutdown from a child's Shutdown = %v, want ErrStopFromOwnWork", nested)
	}
	hostile.Within(t, hostile.Deadline, func() {
		if err := m.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown of the late child = %v, want nil", err)
		}
	})
	if n := late.calls.Load(); n != 1 {
		t.Errorf("late child Shutdown calls = %d, want 1", n)
	}
}

// Shutdowns racing publishes: every child is shut down exactly once (run
// under -race).
func TestChildren_ConcurrentWithPublish(t *testing.T) {
	var m manager
	children := make([]*child, 200)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range children {
			children[i] = &child{}
			m.add(strconv.Itoa(i), children[i])
		}
	}()
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				_ = m.Shutdown(context.Background())
			}
		}()
	}
	wg.Wait()
	_ = m.Shutdown(context.Background())
	for i, c := range children {
		if n := c.calls.Load(); n != 1 {
			t.Errorf("child %d Shutdown calls = %d, want 1", i, n)
		}
	}
}
