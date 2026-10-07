package panicerr

import (
	"errors"
	"io"
	"testing"
)

// A listener's panic reads the same through its mark as the value it
// carries, and only the mark is recognised as a listener's.
func TestListener(t *testing.T) {
	owner, other := new(int), new(int)
	l := NewListener(io.ErrUnexpectedEOF, owner)
	if !IsListenerOf(l, owner) || IsListenerOf(l, other) || IsListenerOf(io.ErrUnexpectedEOF, owner) {
		t.Fatal("IsListenerOf must hold for the owner the mark names and no other")
	}
	if outer := NewListener(l, other); !outer.From(other) || outer.Recovered() != io.ErrUnexpectedEOF {
		t.Fatal("marking a mark must name the new owner and keep the first value")
	}
	if !IsListener(l) || IsListener(io.ErrUnexpectedEOF) || IsListener(nil) {
		t.Fatal("IsListener must recognise the mark and nothing else")
	}
	if l.Recovered() != io.ErrUnexpectedEOF || !errors.Is(l, io.ErrUnexpectedEOF) {
		t.Fatal("the mark does not carry its value")
	}
	if got, want := FromRecovered(l).Error(), FromRecovered(io.ErrUnexpectedEOF).Error(); got != want {
		t.Fatalf("FromRecovered through the mark = %q, want %q", got, want)
	}
	if AsTyped(FromRecovered(l)).Recovered() != io.ErrUnexpectedEOF {
		t.Fatal("FromRecovered kept the mark instead of the value")
	}
	if s := NewListener("boom", owner); s.Unwrap() != nil || s.Error() != FromRecovered("boom").Error() {
		t.Fatalf("a non-error value: Unwrap = %v, Error = %q", s.Unwrap(), s.Error())
	}
}

// A typed nil *Listener is a value like any other, not a mark: user code
// can raise one. Nothing here dereferences it.
func TestListener_TypedNilIsAValueNotAMark(t *testing.T) {
	var none *Listener
	owner := new(int)

	if IsListener(none) {
		t.Fatal("IsListener = true for a typed nil")
	}
	if IsListenerOf(none, owner) || IsListenerOf(none, nil) {
		t.Fatal("IsListenerOf = true for a typed nil")
	}
	marked := NewListener(none, owner)
	if !marked.From(owner) {
		t.Fatal("the mark built around a typed nil does not name its owner")
	}
	if got, ok := marked.Recovered().(*Listener); !ok || got != nil {
		t.Fatalf("Recovered = %#v, want the typed nil the listener raised", marked.Recovered())
	}
	if err := FromRecovered(none); err == nil {
		t.Fatal("FromRecovered = nil for a typed nil value")
	} else {
		_ = err.Error()
	}
	if none.From(owner) || none.Recovered() != nil || none.Unwrap() != nil {
		t.Fatal("a nil *Listener's methods do not answer as an empty value")
	}
	_ = none.Error()
}
