package queue

import (
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/internal/hostile"
)

// Shutdown's wait for the background goroutines closes its done channel
// even when the wait panics and the line reporting it goes to a hostile
// logger: one that panics neither escapes nor skips the close (the line
// reaches the fallback logger), and one that blocks does not hold the
// close, so Shutdown is never held past its ctx by the logger.
func TestMemoryDriver_ShutdownPanicLineIsContained(t *testing.T) {
	const msg = "velocity/queue: memory driver shutdown panic recovered"
	t.Run("panic", func(t *testing.T) {
		out := fallbacklogtest.Capture(t)
		code := hostile.New(t, hostile.Panic, nil)
		d := NewMemoryDriver()
		d.SetLogger(hostile.NewLogger(code, hostile.Error))
		done := make(chan struct{})
		if p := hostile.Within(t, hostile.Deadline, func() {
			d.awaitBackground(func() { panic("wait panicked") }, done)
		}); p != nil {
			t.Fatalf("awaitBackground panicked: %v", p)
		}
		select {
		case <-done:
		default:
			t.Fatal("done was not closed")
		}
		if n := out.Count("ERROR", msg); n != 1 {
			t.Errorf("fallback ERROR lines = %d, want 1:\n%s", n, out.String())
		}
	})
	t.Run("block", func(t *testing.T) {
		code := hostile.New(t, hostile.Block, nil)
		d := NewMemoryDriver()
		d.SetLogger(hostile.NewLogger(code, hostile.Error))
		done := make(chan struct{})
		returned := make(chan struct{})
		go func() { //safe-goroutine: the test releases the blocked logger below and waits for it
			defer close(returned)
			d.awaitBackground(func() { panic("wait panicked") }, done)
		}()
		code.AwaitEntered(t)
		hostile.Within(t, hostile.Deadline, func() { <-done })
		code.Release()
		hostile.Within(t, hostile.Deadline, func() { <-returned })
	})
}
