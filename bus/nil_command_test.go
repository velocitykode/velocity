package bus

import "testing"

// nilPtrCommand is a pointer command: its typed nil is nil.
type nilPtrCommand struct{ Name string }

// A typed nil command is refused like an untyped nil, before it is
// marshalled or pushed: stored, it would cross as "null" and hydrate as
// an empty command that runs.
func TestBus_DispatchAsync_TypedNilCommandRefused(t *testing.T) {
	b := New()
	q := &mockQueuePusher{}
	b.SetQueue(q)
	Register(b, func(cmd *nilPtrCommand) error { return nil })

	for _, cmd := range []Command{nil, (*nilPtrCommand)(nil)} {
		err := b.DispatchAsync(cmd)
		if err == nil || err.Error() != "bus: cannot dispatch nil command" {
			t.Errorf("DispatchAsync(%T) = %v, want the nil-command refusal", cmd, err)
		}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.jobs) != 0 {
		t.Fatalf("%d jobs pushed for nil commands, want 0", len(q.jobs))
	}
}
