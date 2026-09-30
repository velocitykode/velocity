package notification

import (
	"context"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/hostile"
)

// A channel error whose Error panics, a nested panic included, comes back
// from Send and SendMany wrapped, its text errchain.Unreadable: wrapping it
// does not crash the caller.
func TestManager_UnformattableChannelError(t *testing.T) {
	for _, chErr := range []error{hostile.PanicError{}, hostile.PanicError{Nested: true}} {
		m := NewManager()
		m.SetChannel("bad", &testChannel{err: chErr})
		n := &testNotification{channels: []string{"bad"}}
		var sendErr, manyErr error
		if p := hostile.Within(t, hostile.Deadline, func() {
			sendErr = m.Send(context.Background(), "user", n)
			manyErr = m.SendMany(context.Background(), []interface{}{"a", "b"}, n)
		}); p != nil {
			t.Fatalf("nested=%v: a panic escaped: %v", chErr.(hostile.PanicError).Nested, p)
		}
		for _, err := range []error{sendErr, manyErr} {
			if err == nil || !errchain.Is(err, chErr) {
				t.Fatalf("error = %v, want the channel error wrapped", err)
			}
			if text := errchain.Text(err); chErr.(hostile.PanicError).Nested && !strings.Contains(text, errchain.Unreadable) {
				t.Errorf("text = %q, want the channel error as Unreadable", text)
			}
		}
	}
}
