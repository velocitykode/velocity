package events

import (
	"strings"
	"testing"
)

// TestNewAutoSubscriber_RejectsNonStruct covers Task 6d: NewAutoSubscriber
// must reject maps, slices, and primitives where reflection-based method
// discovery is meaningless.
func TestNewAutoSubscriber_RejectsNonStruct(t *testing.T) {
	cases := []struct {
		name     string
		instance interface{}
	}{
		{"nil", nil},
		{"map", map[string]int{}},
		{"slice", []int{}},
		{"int", 42},
		{"string", "hi"},
		{"func", func() {}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("expected panic for non-struct instance")
				}
				msg := ""
				if err, ok := r.(error); ok {
					msg = err.Error()
				}
				if !strings.Contains(msg, "velocity/events") {
					t.Errorf("expected velocity/events prefix, got %q", msg)
				}
			}()
			_ = NewAutoSubscriber(tc.instance, "Handle")
		})
	}
}

// TestNewAutoSubscriber_AcceptsStructAndPointer ensures struct and *struct
// instances are allowed through.
func TestNewAutoSubscriber_AcceptsStructAndPointer(t *testing.T) {
	type S struct{ X int }
	if sub := NewAutoSubscriber(S{}, "Handle"); sub == nil {
		t.Fatal("struct rejected")
	}
	if sub := NewAutoSubscriber(&S{}, "Handle"); sub == nil {
		t.Fatal("*struct rejected")
	}
}

// TestNewMappedSubscriber_RejectsNonStruct parallels the check for mapped
// subscribers.
func TestNewMappedSubscriber_RejectsNonStruct(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil instance")
		}
	}()
	_ = NewMappedSubscriber(nil, EventMap{})
}
