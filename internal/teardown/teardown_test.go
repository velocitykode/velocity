package teardown_test

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/internal/panicerr"
	"github.com/velocitykode/velocity/internal/teardown"
)

// Step returns the step's own result when it does not panic.
func TestStep_ReturnsTheStepsError(t *testing.T) {
	if err := teardown.Step(func() error { return nil }); err != nil {
		t.Fatalf("Step(nil step result) = %v, want nil", err)
	}
	want := errors.New("close failed")
	if err := teardown.Step(func() error { return want }); err != want {
		t.Fatalf("Step = %v, want the step's error itself", err)
	}
}

// A panicking step becomes its error: the caller goes on to the next
// step, and the raw recovered value is kept.
func TestStep_ContainsAPanic(t *testing.T) {
	type custom struct{ n int }
	var ran []string
	var errs []error
	for _, s := range []struct {
		name string
		fn   func() error
	}{
		{"first", func() error { panic(custom{7}) }},
		{"second", func() error { return nil }},
	} {
		ran = append(ran, s.name)
		errs = append(errs, teardown.Step(s.fn))
	}
	if len(ran) != 2 {
		t.Fatalf("steps run = %v, want both", ran)
	}
	pe := panicerr.AsTyped(errs[0])
	if pe == nil {
		t.Fatalf("Step of a panicking step = %v, want a *panicerr.Error", errs[0])
	}
	if got, ok := pe.Recovered().(custom); !ok || got.n != 7 {
		t.Errorf("Recovered = %#v, want the raw panic value", pe.Recovered())
	}
	if errs[1] != nil {
		t.Errorf("the step after the panic returned %v, want nil", errs[1])
	}
}

// A step that panics with an error keeps it in the chain.
func TestStep_PanicWithAnErrorUnwraps(t *testing.T) {
	sentinel := errors.New("boom")
	err := teardown.Step(func() error { panic(sentinel) })
	if !errors.Is(err, sentinel) {
		t.Fatalf("errors.Is(Step, sentinel) = false for %v", err)
	}
}

// The panic value is not formatted by Step: a value whose String blocks
// or panics cannot wedge or crash the step's caller.
func TestStep_DoesNotFormatThePanicValue(t *testing.T) {
	err := teardown.Step(func() error { panic(hostileStringer{}) })
	if panicerr.AsTyped(err) == nil {
		t.Fatalf("Step = %T, want a *panicerr.Error", err)
	}
}

type hostileStringer struct{}

func (hostileStringer) String() string { panic("String must not be called by Step") }

// runtime.Goexit is not a panic: Step does not swallow it.
func TestStep_GoexitEndsTheGoroutine(t *testing.T) {
	done := make(chan bool)
	go func() {
		returned := false
		defer func() { done <- returned }()
		_ = teardown.Step(func() error { runtime.Goexit(); return nil })
		returned = true
	}()
	if <-done {
		t.Fatal("Step returned after Goexit; the goroutine should have ended")
	}
}

// teardown sits under every manager, so it imports only the standard
// library, contract and the internal leaves drain, errchain, fallbacklog,
// nilval and panicerr.
func TestTeardownImportsOnlyItsLeaves(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not on PATH")
	}
	out, err := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	allowed := map[string]bool{
		"github.com/velocitykode/velocity/contract":             true,
		"github.com/velocitykode/velocity/internal/drain":       true,
		"github.com/velocitykode/velocity/internal/errchain":    true,
		"github.com/velocitykode/velocity/internal/fallbacklog": true,
		"github.com/velocitykode/velocity/internal/nilval":      true,
		"github.com/velocitykode/velocity/internal/panicerr":    true,
	}
	for _, imp := range strings.Fields(string(out)) {
		if strings.Contains(imp, ".") && !allowed[imp] {
			t.Errorf("internal/teardown imports %s; it may import only the standard library, contract, internal/drain, internal/errchain, internal/fallbacklog, internal/nilval and internal/panicerr", imp)
		}
	}
}

type closer struct {
	ctx   context.Context
	calls int
	err   error
	panic any
}

func (c *closer) Shutdown(ctx context.Context) error {
	c.calls++
	c.ctx = ctx
	if c.panic != nil {
		panic(c.panic)
	}
	return c.err
}

// Close shuts down a value that has Shutdown, with the caller's ctx, and
// returns its error.
func TestClose_ShutsDownWithTheCallersContext(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "v")
	want := errors.New("close failed")
	c := &closer{err: want}
	if err := teardown.Close(ctx, c); err != want {
		t.Fatalf("Close = %v, want the Shutdown error", err)
	}
	if c.calls != 1 || c.ctx != ctx {
		t.Fatalf("Shutdown calls = %d, ctx passed = %v", c.calls, c.ctx == ctx)
	}
}

// Close contains a panicking Shutdown.
func TestClose_ContainsAPanic(t *testing.T) {
	c := &closer{panic: "boom"}
	err := teardown.Close(context.Background(), c)
	if pe := panicerr.AsTyped(err); pe == nil || pe.Recovered() != "boom" {
		t.Fatalf("Close = %v, want the panic as a *panicerr.Error", err)
	}
}

// Close ignores a value with no Shutdown, and nil.
func TestClose_NothingToShutDown(t *testing.T) {
	for _, v := range []any{nil, 42, struct{}{}} {
		if err := teardown.Close(context.Background(), v); err != nil {
			t.Errorf("Close(%#v) = %v, want nil", v, err)
		}
	}
}

// A nil pointer whose Shutdown dereferences it is a contained panic.
func TestClose_NilPointerChild(t *testing.T) {
	var c *closer
	if err := teardown.Close(context.Background(), c); panicerr.AsTyped(err) == nil {
		t.Fatalf("Close(nil *closer) = %v, want a contained panic", err)
	}
}

// onlyCloser closes through Close() error alone.
type onlyCloser struct {
	calls int
	err   error
}

func (c *onlyCloser) Close() error {
	c.calls++
	return c.err
}

// both has Shutdown and Close: Shutdown is its closer.
type both struct {
	shutdowns, closes int
}

func (b *both) Shutdown(context.Context) error { b.shutdowns++; return nil }
func (b *both) Close() error                   { b.closes++; return nil }

// valueCloser has a value-receiver Shutdown: reaching it through a nil
// pointer panics.
type valueCloser struct{}

func (valueCloser) Shutdown(context.Context) error { return nil }

// The closer contract: Close() error and func(ctx) error close too;
// Shutdown wins over Close.
func TestClose_TheCloserContract(t *testing.T) {
	want := errors.New("close failed")
	oc := &onlyCloser{err: want}
	if err := teardown.Close(context.Background(), oc); err != want || oc.calls != 1 {
		t.Errorf("Close of a Close() error value = %v after %d calls, want its error after 1", err, oc.calls)
	}
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "v")
	var got context.Context
	fn := func(c context.Context) error { got = c; return want }
	if err := teardown.Close(ctx, fn); err != want || got != ctx {
		t.Errorf("Close of a func = %v (ctx passed %v), want its error with the caller's ctx", err, got == ctx)
	}
	b := &both{}
	if err := teardown.Close(context.Background(), b); err != nil || b.shutdowns != 1 || b.closes != 0 {
		t.Errorf("Close of a value with Shutdown and Close = %v, Shutdown %d, Close %d; want Shutdown only", err, b.shutdowns, b.closes)
	}
	var nilFn func(context.Context) error
	if err := teardown.Close(context.Background(), nilFn); err != nil {
		t.Errorf("Close of a nil func = %v, want nil (nothing to close)", err)
	}
	var vc *valueCloser
	if err := teardown.Close(context.Background(), vc); panicerr.AsTyped(err) == nil {
		t.Errorf("Close of a nil pointer with a value-receiver Shutdown = %v, want a contained panic", err)
	}
}

// drainStop is a stop that drains work: at ctx it returns ctx's error
// while the work goes on, and every later call waits for the work and
// returns its result.
type drainStop struct {
	calls   atomic.Int32
	started sync.Once
	done    chan struct{}
	release chan struct{}
	err     error
}

func newDrainStop(err error) *drainStop {
	return &drainStop{done: make(chan struct{}), release: make(chan struct{}), err: err}
}

func (d *drainStop) stop(ctx context.Context) error {
	d.calls.Add(1)
	d.started.Do(func() {
		go func() {
			<-d.release
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

// At a done ctx, Drain waits for the stop's work and returns the stop's
// retained result, not the ctx's error.
func TestDrain_ReWaitsPastTheDeadline(t *testing.T) {
	want := errors.New("work failed")
	d := newDrainStop(want)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := make(chan error, 1)
	go func() { result <- teardown.Drain(ctx, d.stop) }()
	close(d.release)
	var err error
	hostile.Within(t, hostile.Deadline, func() { err = <-result })
	if err != want {
		t.Fatalf("Drain = %v, want the stop's retained result", err)
	}
	if n := d.calls.Load(); n != 2 {
		t.Errorf("stop calls = %d, want 2 (the stop, then the detached re-wait)", n)
	}
}

// With ctx live, or a stop that succeeds, Drain calls the stop once.
func TestDrain_OneCallWhenNoReWaitIsNeeded(t *testing.T) {
	want := errors.New("failed")
	calls := 0
	if err := teardown.Drain(context.Background(), func(context.Context) error { calls++; return want }); err != want || calls != 1 {
		t.Errorf("Drain with a live ctx = %v after %d calls, want the error after 1", err, calls)
	}
	calls = 0
	if err := teardown.Drain(cancelledCtx(), func(context.Context) error { calls++; return nil }); err != nil || calls != 1 {
		t.Errorf("Drain of a stop that succeeded = %v after %d calls, want nil after 1", err, calls)
	}
	calls = 0
	var nilCtx context.Context // a nil ctx is part of Drain's contract
	if err := teardown.Drain(nilCtx, func(ctx context.Context) error { calls++; return ctx.Err() }); err != nil || calls != 1 {
		t.Errorf("Drain with a nil ctx = %v after %d calls, want nil after 1", err, calls)
	}
}

// A panicking stop is contained and not called again.
func TestDrain_PanicIsNotRepeated(t *testing.T) {
	calls := 0
	err := teardown.Drain(cancelledCtx(), func(context.Context) error { calls++; panic("stop panicked") })
	if pe := panicerr.AsTyped(err); pe == nil || calls != 1 {
		t.Fatalf("Drain of a panicking stop = %v after %d calls, want the contained panic after 1", err, calls)
	}
}

func cancelledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
