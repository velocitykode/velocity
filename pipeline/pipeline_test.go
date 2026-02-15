package pipeline

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestBasicFlow(t *testing.T) {
	var order []string

	a := Pipe[string](func(s string, next func(string) error) error {
		order = append(order, "a")
		return next(s)
	})
	b := Pipe[string](func(s string, next func(string) error) error {
		order = append(order, "b")
		return next(s)
	})
	c := Pipe[string](func(s string, next func(string) error) error {
		order = append(order, "c")
		return next(s)
	})

	err := New[string]().Send("x").Through(a, b, c).Then(func(s string) error {
		order = append(order, "dest")
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := strings.Join(order, ",")
	want := "a,b,c,dest"
	if got != want {
		t.Errorf("order = %q, want %q", got, want)
	}
}

func TestShortCircuit(t *testing.T) {
	var order []string

	a := Pipe[string](func(s string, next func(string) error) error {
		order = append(order, "a")
		return nil // don't call next
	})
	b := Pipe[string](func(s string, next func(string) error) error {
		order = append(order, "b")
		return next(s)
	})

	err := New[string]().Send("x").Through(a, b).Then(func(s string) error {
		order = append(order, "dest")
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := strings.Join(order, ",")
	want := "a"
	if got != want {
		t.Errorf("order = %q, want %q", got, want)
	}
}

func TestPassableMutation(t *testing.T) {
	type data struct{ value int }

	double := Pipe[*data](func(d *data, next func(*data) error) error {
		d.value *= 2
		return next(d)
	})
	addTen := Pipe[*data](func(d *data, next func(*data) error) error {
		d.value += 10
		return next(d)
	})

	d := &data{value: 5}
	var finalValue int

	err := New[*data]().Send(d).Through(double, addTen).Then(func(d *data) error {
		finalValue = d.value
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 5 * 2 = 10, 10 + 10 = 20
	if finalValue != 20 {
		t.Errorf("finalValue = %d, want 20", finalValue)
	}
}

func TestErrorFromPipe(t *testing.T) {
	pipeErr := errors.New("pipe failed")

	failing := Pipe[string](func(s string, next func(string) error) error {
		return pipeErr
	})
	unreachable := Pipe[string](func(s string, next func(string) error) error {
		t.Fatal("should not be reached")
		return next(s)
	})

	err := New[string]().Send("x").Through(failing, unreachable).Then(func(s string) error {
		t.Fatal("destination should not be reached")
		return nil
	})

	if !errors.Is(err, pipeErr) {
		t.Errorf("err = %v, want %v", err, pipeErr)
	}
}

func TestErrorFromDestination(t *testing.T) {
	destErr := errors.New("dest failed")

	passthrough := Pipe[string](func(s string, next func(string) error) error {
		return next(s)
	})

	err := New[string]().Send("x").Through(passthrough).Then(func(s string) error {
		return destErr
	})

	if !errors.Is(err, destErr) {
		t.Errorf("err = %v, want %v", err, destErr)
	}
}

func TestEmptyPipeline(t *testing.T) {
	called := false
	err := New[string]().Send("hello").Then(func(s string) error {
		called = true
		if s != "hello" {
			t.Errorf("passable = %q, want %q", s, "hello")
		}
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("destination was not called")
	}
}

func TestThenReturn(t *testing.T) {
	var order []string

	a := Pipe[string](func(s string, next func(string) error) error {
		order = append(order, "a")
		return next(s)
	})

	err := New[string]().Send("x").Through(a).ThenReturn()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := strings.Join(order, ",")
	if got != "a" {
		t.Errorf("order = %q, want %q", got, "a")
	}
}

func TestAddAppend(t *testing.T) {
	var order []string
	makePipe := func(name string) Pipe[string] {
		return func(s string, next func(string) error) error {
			order = append(order, name)
			return next(s)
		}
	}

	err := New[string]().Send("x").
		Through(makePipe("a"), makePipe("b")).
		Add(makePipe("c")).
		Then(func(s string) error {
			order = append(order, "dest")
			return nil
		})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := strings.Join(order, ",")
	want := "a,b,c,dest"
	if got != want {
		t.Errorf("order = %q, want %q", got, want)
	}
}

func TestTypedPipeline(t *testing.T) {
	type Order struct {
		Total    float64
		Discount float64
		Tax      float64
	}

	applyDiscount := Pipe[*Order](func(o *Order, next func(*Order) error) error {
		o.Total -= o.Discount
		return next(o)
	})
	applyTax := Pipe[*Order](func(o *Order, next func(*Order) error) error {
		o.Total += o.Tax
		return next(o)
	})

	order := &Order{Total: 100, Discount: 15, Tax: 8.5}
	err := New[*Order]().Send(order).Through(applyDiscount, applyTax).ThenReturn()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 100 - 15 = 85, 85 + 8.5 = 93.5
	if order.Total != 93.5 {
		t.Errorf("Total = %f, want 93.5", order.Total)
	}
}

func TestErrorPropagationThroughChain(t *testing.T) {
	innerErr := errors.New("inner error")

	outer := Pipe[string](func(s string, next func(string) error) error {
		err := next(s)
		if err != nil {
			return fmt.Errorf("outer caught: %w", err)
		}
		return nil
	})
	inner := Pipe[string](func(s string, next func(string) error) error {
		return innerErr
	})

	err := New[string]().Send("x").Through(outer, inner).ThenReturn()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, innerErr) {
		t.Errorf("err should wrap innerErr, got: %v", err)
	}
	if !strings.Contains(err.Error(), "outer caught") {
		t.Errorf("err should contain 'outer caught', got: %v", err)
	}
}

// Struct-based stages — the Laravel-style API

type validateOrder struct{}

func (v *validateOrder) Handle(o *testOrder, next func(*testOrder) error) error {
	if o.Total <= 0 {
		return errors.New("invalid total")
	}
	o.Validated = true
	return next(o)
}

type applyCoupon struct {
	discount float64
}

func (a *applyCoupon) Handle(o *testOrder, next func(*testOrder) error) error {
	o.Total -= a.discount
	return next(o)
}

type calculateTax struct {
	rate float64
}

func (c *calculateTax) Handle(o *testOrder, next func(*testOrder) error) error {
	o.Tax = o.Total * c.rate
	o.Total += o.Tax
	return next(o)
}

type testOrder struct {
	Total     float64
	Tax       float64
	Validated bool
}

func TestStructStages(t *testing.T) {
	order := &testOrder{Total: 100}

	err := New[*testOrder]().
		Send(order).
		Through(
			&validateOrder{},
			&applyCoupon{discount: 20},
			&calculateTax{rate: 0.1},
		).
		ThenReturn()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !order.Validated {
		t.Error("order should be validated")
	}
	// 100 - 20 = 80, tax = 8, total = 88
	if order.Total != 88 {
		t.Errorf("Total = %f, want 88", order.Total)
	}
	if order.Tax != 8 {
		t.Errorf("Tax = %f, want 8", order.Tax)
	}
}

func TestStructStageShortCircuit(t *testing.T) {
	order := &testOrder{Total: -5}

	err := New[*testOrder]().
		Send(order).
		Through(
			&validateOrder{},
			&applyCoupon{discount: 10},
		).
		ThenReturn()

	if err == nil {
		t.Fatal("expected validation error")
	}
	if err.Error() != "invalid total" {
		t.Errorf("err = %q, want %q", err.Error(), "invalid total")
	}
}

func TestMixedFuncAndStructStages(t *testing.T) {
	var order []string

	logStage := Pipe[string](func(s string, next func(string) error) error {
		order = append(order, "func-pipe")
		return next(s)
	})

	err := New[string]().
		Send("x").
		Through(logStage, &appendStage{name: "struct-stage", order: &order}).
		Then(func(s string) error {
			order = append(order, "dest")
			return nil
		})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := strings.Join(order, ",")
	want := "func-pipe,struct-stage,dest"
	if got != want {
		t.Errorf("order = %q, want %q", got, want)
	}
}

type appendStage struct {
	name  string
	order *[]string
}

func (a *appendStage) Handle(s string, next func(string) error) error {
	*a.order = append(*a.order, a.name)
	return next(s)
}

// Build — pre-compile and reuse

func TestBuild(t *testing.T) {
	var calls int

	counter := Pipe[string](func(s string, next func(string) error) error {
		calls++
		return next(s)
	})

	compiled := New[string]().
		Through(counter).
		Build(func(s string) error { return nil })

	// Invoke the compiled chain multiple times
	for i := 0; i < 5; i++ {
		if err := compiled("hello"); err != nil {
			t.Fatalf("call %d: unexpected error: %v", i, err)
		}
	}
	if calls != 5 {
		t.Errorf("calls = %d, want 5", calls)
	}
}

func TestBuildMultiStage(t *testing.T) {
	var order []string

	a := Pipe[string](func(s string, next func(string) error) error {
		order = append(order, "a")
		return next(s)
	})
	b := Pipe[string](func(s string, next func(string) error) error {
		order = append(order, "b")
		return next(s)
	})
	c := Pipe[string](func(s string, next func(string) error) error {
		order = append(order, "c")
		return next(s)
	})

	compiled := New[string]().
		Through(a, b, c).
		Build(func(s string) error {
			order = append(order, "dest:"+s)
			return nil
		})

	// Invoke multiple times — chain must be stable across calls
	for i := 0; i < 3; i++ {
		order = nil
		if err := compiled("run"); err != nil {
			t.Fatalf("call %d: unexpected error: %v", i, err)
		}
		got := strings.Join(order, ",")
		want := "a,b,c,dest:run"
		if got != want {
			t.Errorf("call %d: order = %q, want %q", i, got, want)
		}
	}
}

func TestBuildEmpty(t *testing.T) {
	called := false
	compiled := New[string]().Build(func(s string) error {
		called = true
		if s != "hi" {
			t.Errorf("passable = %q, want %q", s, "hi")
		}
		return nil
	})

	if err := compiled("hi"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("destination was not called")
	}
}

// Through() replaces semantics

func TestThroughReplaces(t *testing.T) {
	var order []string
	makePipe := func(name string) Pipe[string] {
		return func(s string, next func(string) error) error {
			order = append(order, name)
			return next(s)
		}
	}

	// Second Through replaces the first
	err := New[string]().Send("x").
		Through(makePipe("a"), makePipe("b")).
		Through(makePipe("c")).
		Then(func(s string) error {
			order = append(order, "dest")
			return nil
		})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := strings.Join(order, ",")
	want := "c,dest"
	if got != want {
		t.Errorf("order = %q, want %q (Through should replace, not append)", got, want)
	}
}

// Nil passable

func TestNilPassable(t *testing.T) {
	type data struct{ value int }

	stage := Pipe[*data](func(d *data, next func(*data) error) error {
		if d == nil {
			return errors.New("nil passable")
		}
		return next(d)
	})

	err := New[*data]().Send(nil).Through(stage).ThenReturn()
	if err == nil {
		t.Fatal("expected error for nil passable")
	}
	if err.Error() != "nil passable" {
		t.Errorf("err = %q, want %q", err.Error(), "nil passable")
	}
}

func TestNilPassableReachesDestination(t *testing.T) {
	type data struct{ value int }

	passthrough := Pipe[*data](func(d *data, next func(*data) error) error {
		return next(d)
	})

	var received *data
	err := New[*data]().Send(nil).Through(passthrough).Then(func(d *data) error {
		received = d
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if received != nil {
		t.Errorf("received = %v, want nil", received)
	}
}
