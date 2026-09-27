package mail

import (
	"context"
	"testing"
)

// nopMailer accepts every message and records nothing.
type nopMailer struct{}

func (nopMailer) Send(context.Context, *Message) error { return nil }

// TestManager_NoDispatcherBuildsNoEvent requires a send to build no
// MailSent event when no event dispatcher is installed: it allocates less
// than the same send with a dispatcher that discards every event.
func TestManager_NoDispatcherBuildsNoEvent(t *testing.T) {
	m := NewManager()
	m.SetChannel("nop", nopMailer{})
	msg := NewMessage().To("a@example.test").Subject("s").Body("b")
	ctx := context.Background()
	send := func() { _ = m.Send(ctx, "nop", msg) }

	without := testing.AllocsPerRun(100, send)
	m.SetEventDispatcher(func(context.Context, interface{}) error { return nil })
	with := testing.AllocsPerRun(100, send)
	if without >= with {
		t.Errorf("send allocated %.0f times with no dispatcher and %.0f with one, want fewer without", without, with)
	}
}
