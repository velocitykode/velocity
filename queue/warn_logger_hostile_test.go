package queue

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/internal/hostile"
)

// The one-time warning for a job without an id is written through the
// installed logger while the job is pushed: a logger whose With or Warn
// panics must not fail the push or escape into the caller, and the
// warning reaches the fallback logger instead.
func TestMemoryDriver_NonIdentifiableWarningIsContained(t *testing.T) {
	for _, method := range []hostile.Method{hostile.With, hostile.Warn} {
		t.Run(string(method), func(t *testing.T) {
			out := fallbacklogtest.Capture(t)
			code := hostile.New(t, hostile.Panic, nil)
			d := NewMemoryDriver()
			d.SetLogger(hostile.NewLogger(code, method))
			t.Cleanup(func() { _ = d.Shutdown(context.Background()) })

			var err error
			if p := hostile.Within(t, hostile.Deadline, func() {
				err = d.PushCtx(context.Background(), fallbackProbeJob{})
			}); p != nil {
				t.Fatalf("PushCtx panicked: %v", p)
			}
			if err != nil {
				t.Fatalf("PushCtx: %v", err)
			}
			if n, _ := d.Size("default"); n != 1 {
				t.Errorf("queued jobs = %d, want 1", n)
			}
			if n := out.Count("WARN", "velocity/queue: job type does not implement Identifiable"); n != 1 {
				t.Errorf("fallback WARN lines = %d, want 1:\n%s", n, out.String())
			}
		})
	}
}
