package teardown_test

import (
	"sync"
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/internal/teardown"
)

// ownsCallerPanicLine is the warning Owners writes for a panic.
const ownsCallerPanicLine = "velocity: OwnsCaller panicked"

// answer is a component whose OwnsCaller runs code, then answers owns.
type answer struct {
	code *hostile.Code
	owns bool
}

func (a *answer) OwnsCaller() bool {
	a.code.Run()
	return a.owns
}

// valueAnswer is a component of a type Go cannot compare.
type valueAnswer struct {
	tags map[string]string
	code *hostile.Code
}

func (a valueAnswer) OwnsCaller() bool {
	a.code.Run()
	return true
}

// Owns takes the component's answer, and false from what cannot answer.
func TestOwners_Owns(t *testing.T) {
	out := fallbacklogtest.Capture(t)
	var o teardown.Owners
	tests := []struct {
		name string
		v    any
		want bool
	}{
		{"nil", nil, false},
		{"typed nil", (*answer)(nil), false},
		{"no OwnsCaller", struct{}{}, false},
		{"answers false", &answer{}, false},
		{"answers true", &answer{owns: true}, true},
		{"non-comparable, answers true", valueAnswer{tags: map[string]string{}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got bool
			if p := hostile.Within(t, hostile.Deadline, func() { got = o.Owns(tt.v) }); p != nil {
				t.Fatalf("Owns panicked: %v", p)
			}
			if got != tt.want {
				t.Errorf("Owns = %v, want %v", got, tt.want)
			}
		})
	}
	if n := out.Count("WARN", ownsCallerPanicLine); n != 0 {
		t.Errorf("%d warnings with no panic, want 0; output:\n%s", n, out)
	}
}

// A panicking OwnsCaller is not an answer, and is written once per
// component: per instance, or per type for a value Go cannot compare.
func TestOwners_APanicIsContainedAndReportedOncePerComponent(t *testing.T) {
	out := fallbacklogtest.Capture(t)
	var o teardown.Owners
	ask := func(v any) {
		t.Helper()
		var got bool
		if p := hostile.Within(t, hostile.Deadline, func() { got = o.Owns(v) }); p != nil {
			t.Fatalf("Owns panicked: %v", p)
		}
		if got {
			t.Fatal("Owns of a panicking component = true, want false")
		}
	}
	first := &answer{code: hostile.New(t, hostile.Panic, nil), owns: true}
	for range 3 {
		ask(first)
	}
	if n := out.Count("WARN", ownsCallerPanicLine); n != 1 {
		t.Fatalf("%d warnings after three asks of one instance, want 1; output:\n%s", n, out)
	}
	ask(&answer{code: hostile.New(t, hostile.Panic, nil)})
	if n := out.Count("WARN", ownsCallerPanicLine); n != 2 {
		t.Fatalf("%d warnings after a second instance, want 2; output:\n%s", n, out)
	}
	for range 3 {
		ask(valueAnswer{tags: map[string]string{}, code: hostile.New(t, hostile.Panic, nil)})
	}
	if n := out.Count("WARN", ownsCallerPanicLine); n != 3 {
		t.Fatalf("%d warnings after three non-comparable values of one type, want 3 in total; output:\n%s", n, out)
	}
}

// Concurrent asks of one panicking component write one warning.
func TestOwners_Concurrent(t *testing.T) {
	out := fallbacklogtest.Capture(t)
	var o teardown.Owners
	hostileAnswer := &answer{code: hostile.New(t, hostile.Panic, nil)}
	owner := &answer{owns: true}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				if o.Owns(hostileAnswer) {
					t.Error("Owns of a panicking component = true, want false")
				}
				if !o.Owns(owner) {
					t.Error("Owns = false, want the component's answer, true")
				}
			}
		}()
	}
	hostile.Within(t, hostile.Deadline, wg.Wait)
	if n := out.Count("WARN", ownsCallerPanicLine); n != 1 {
		t.Fatalf("%d warnings from concurrent asks of one instance, want 1; output:\n%s", n, out)
	}
}
