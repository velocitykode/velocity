package teardown_test

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"

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
// library and internal/panicerr.
func TestTeardownImportsOnlyItsLeaves(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not on PATH")
	}
	out, err := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, imp := range strings.Fields(string(out)) {
		if strings.Contains(imp, ".") && imp != "github.com/velocitykode/velocity/internal/panicerr" {
			t.Errorf("internal/teardown imports %s; it may import only the standard library and internal/panicerr", imp)
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
