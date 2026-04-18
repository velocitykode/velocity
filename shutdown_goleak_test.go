package velocity

import (
	"context"
	"runtime"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// TestBootShutdown_NoGoroutineLeaks exercises the full boot + shutdown
// path against an in-memory test app and asserts no goroutines outlive
// Shutdown. Uses go.uber.org/goleak to detect any long-lived worker,
// ticker, or cleanup goroutine that forgets to honour the shutdown
// signal.
//
// Ignored goroutines:
//   - goleak's own inspector goroutines.
//   - testing.tRunner, which owns every test goroutine in this process
//     and only exits when the test binary does.
func TestBootShutdown_NoGoroutineLeaks(t *testing.T) {
	defer goleak.VerifyNone(t,
		goleak.IgnoreTopFunction("testing.tRunner"),
		goleak.IgnoreTopFunction("testing.(*T).Run"),
		goleak.IgnoreCurrent(),
	)

	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	if err := a.bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	// Wait for the goroutine count to stabilize before goleak samples.
	// Shutdown is mostly synchronous, but async.Go workers observe ctx.Done
	// and unwind their own stacks — we poll until two consecutive samples
	// agree, so a slow worker is detected as a leak rather than masked by
	// a fixed sleep.
	waitForGoroutinesToSettle(t, 2*time.Second)
}

func waitForGoroutinesToSettle(t *testing.T, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	prev := runtime.NumGoroutine()
	for time.Now().Before(deadline) {
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond)
		cur := runtime.NumGoroutine()
		if cur == prev {
			return
		}
		prev = cur
	}
}
