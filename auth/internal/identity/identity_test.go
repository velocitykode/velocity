package identity

import (
	"errors"
	"fmt"
	"testing"
)

type userOf func() interface{}

func (f userOf) GetAuthIdentifier() interface{} { return f() }

type textID string

func (t textID) String() string { return "id-" + string(t) }

type panickingID struct{}

func (panickingID) String() string { panic("String called") }

// Of returns the identifier and its %v text, the fast paths agreeing with
// fmt, and ErrUnreadable with no text when either call panics.
func TestOf(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   func() interface{}
	}{
		{"string", func() interface{} { return "u1" }},
		{"uint", func() interface{} { return uint(7) }},
		{"uint64", func() interface{} { return uint64(1 << 63) }},
		{"int", func() interface{} { return -3 }},
		{"int64", func() interface{} { return int64(42) }},
		{"stringer", func() interface{} { return textID("x") }},
		{"nil", func() interface{} { return nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, text, err := Of(userOf(tc.id))
			want := tc.id()
			if err != nil || id != want || text != fmt.Sprint(want) {
				t.Fatalf("Of = %v, %q, %v; want %v, %q, nil", id, text, err, want, fmt.Sprint(want))
			}
		})
	}
	for _, tc := range []struct {
		name string
		id   func() interface{}
	}{
		{"GetAuthIdentifier panics", func() interface{} { panic("GetAuthIdentifier called") }},
		{"String panics", func() interface{} { return panickingID{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, text, err := Of(userOf(tc.id))
			if !errors.Is(err, ErrUnreadable) || id != nil || text != "" {
				t.Fatalf("Of = %v, %q, %v; want nil, \"\", ErrUnreadable", id, text, err)
			}
		})
	}
}
