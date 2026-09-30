package mail

import (
	"context"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/hostile"
)

// errMailer fails every send with err.
type errMailer struct{ err error }

func (m errMailer) Send(context.Context, *Message) error { return m.err }

// A driver error whose Error panics, a nested panic included, comes back
// from Send wrapped, and in Broadcast's error its text reads
// errchain.Unreadable: formatting it does not crash the caller or a
// broadcast goroutine.
func TestManager_UnformattableDriverError(t *testing.T) {
	for _, drvErr := range []error{hostile.PanicError{}, hostile.PanicError{Nested: true}} {
		m := NewManager()
		m.SetChannel("bad", errMailer{err: drvErr})
		var sendErr, broadcastErr error
		if p := hostile.Within(t, hostile.Deadline, func() {
			sendErr = m.Send(context.Background(), "bad", NewMessage().To("a@example.com").Subject("s"))
			broadcastErr = m.Broadcast(context.Background(), []string{"bad"}, NewMessage().To("a@example.com").Subject("s"))
		}); p != nil {
			t.Fatalf("a panic escaped: %v", p)
		}
		if !errchain.Is(sendErr, drvErr) {
			t.Fatalf("Send error = %q, want the driver error wrapped", errchain.Text(sendErr))
		}
		if text := errchain.Text(broadcastErr); drvErr.(hostile.PanicError).Nested && !strings.Contains(text, errchain.Unreadable) {
			t.Errorf("broadcast text = %q, want the driver error as Unreadable", text)
		}
	}
}
