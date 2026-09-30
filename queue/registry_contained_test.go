package queue

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// countedError is a factory error whose Error method is user code: it
// counts its calls and panics when told to.
type countedError struct {
	calls atomic.Int32
	panic bool
}

func (e *countedError) Error() string {
	e.calls.Add(1)
	if e.panic {
		panic(hostile.PanicValue)
	}
	return "factory broke"
}

// The registry's factory is user code, and so is the Error method of the
// error it returns. Deserialize contains both: a panic in either comes back
// as the fixed-text rebuild panic with the recovered value reachable, and a
// factory error's text is read once, inside the containment, so every
// later Error call on what Deserialize returns runs no user code. The
// factory's error stays reachable through Unwrap.
func TestJobRegistry_DeserializeContainsTheFactory(t *testing.T) {
	const typ = "containedFactoryJob"
	cases := []struct {
		name      string
		factory   func(*countedError) func([]byte) (Job, error)
		errPanics bool
		wantText  string
		wantCause bool
	}{
		{"factory panics", func(*countedError) func([]byte) (Job, error) {
			return func([]byte) (Job, error) { panic(hostile.PanicValue) }
		}, false, errHydrationPanicked, false},
		{"factory error", func(e *countedError) func([]byte) (Job, error) {
			return func([]byte) (Job, error) { return nil, e }
		}, false, "factory broke", true},
		{"factory error whose Error panics", func(e *countedError) func([]byte) (Job, error) {
			return func([]byte) (Job, error) { return nil, e }
		}, true, errHydrationPanicked, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			userErr := &countedError{panic: c.errPanics}
			r := &JobRegistry{handlers: map[string]func([]byte) (Job, error){typ: c.factory(userErr)}}

			var (
				job Job
				err error
			)
			if escaped := hostile.Within(t, hostile.Deadline, func() {
				job, err = r.Deserialize(&Payload{Type: typ, Data: []byte(`{}`)})
			}); escaped != nil {
				t.Fatalf("Deserialize let a panic reach the caller: %v", escaped)
			}
			if job != nil || err == nil {
				t.Fatalf("Deserialize = %v, %v; want no job and an error", job, err)
			}
			before := userErr.calls.Load()
			for range 3 {
				if got := err.Error(); got != c.wantText {
					t.Fatalf("Error() = %q, want %q", got, c.wantText)
				}
			}
			if after := userErr.calls.Load(); after != before {
				t.Errorf("Error() on the returned error called the factory error's Error %d more times; want 0", after-before)
			}
			if c.wantCause && !errors.Is(err, userErr) {
				t.Errorf("errors.Is(err, factory error) = false; want the cause reachable")
			}
			if c.wantText == errHydrationPanicked {
				if pe := panicerr.AsTyped(err); pe == nil || pe.Recovered() != hostile.PanicValue {
					t.Errorf("err = %v; want the recovered panic as *panicerr.Error", err)
				}
			}
		})
	}
}

// BenchmarkJobRegistry_Deserialize measures rebuilding a job through the
// registry, the step every durable pop runs.
func BenchmarkJobRegistry_Deserialize(b *testing.B) {
	const typ = "benchContainedJob"
	r := &JobRegistry{handlers: map[string]func([]byte) (Job, error){
		typ: func([]byte) (Job, error) { return &hydrateHostileJob{}, nil },
	}}
	p := &Payload{Type: typ, Data: []byte(`{}`)}
	b.Run("serial", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := r.Deserialize(p); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if _, err := r.Deserialize(p); err != nil {
					b.Error(err)
					return
				}
			}
		})
	})
}
