package redis

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/queue"
)

// The redis driver's warnings go through the installed logger, which is
// user code: one that panics must not escape into the constructor or a
// pop, and each warning reaches the fallback logger instead.
func TestWarnIfInsecure_PanickingLoggerIsContained(t *testing.T) {
	out := captureWarnings(t)
	code := hostile.New(t, hostile.Panic, nil)
	if p := hostile.Within(t, hostile.Deadline, func() {
		warnIfInsecure(hostile.NewLogger(code, hostile.Warn), "redis.internal", "", false)
	}); p != nil {
		t.Fatalf("warnIfInsecure panicked: %v", p)
	}
	for _, msg := range []string{
		"velocity/queue: redis driver connecting to non-loopback host without TLS",
		"velocity/queue: redis driver connecting to non-loopback host without a password",
	} {
		if n := out.Count("WARN", msg); n != 1 {
			t.Errorf("fallback WARN lines for %q = %d, want 1:\n%s", msg, n, out.String())
		}
	}
}

func TestRedisDriver_NonIdentifiableWarningIsContained(t *testing.T) {
	for _, method := range []hostile.Method{hostile.With, hostile.Warn} {
		t.Run(string(method), func(t *testing.T) {
			driver, _ := newMiniRedisDriver(t)
			out := captureWarnings(t)
			code := hostile.New(t, hostile.Panic, nil)
			driver.SetLogger(hostile.NewLogger(code, method))

			if err := driver.PushCtx(context.Background(), &redisAttemptsJob{ID: "job-1"}, "hostile-warn"); err != nil {
				t.Fatalf("PushCtx: %v", err)
			}
			var job queue.Job
			var err error
			if p := hostile.Within(t, hostile.Deadline, func() {
				job, _, err = driver.PopCtxWithTrace(context.Background(), "hostile-warn")
			}); p != nil {
				t.Fatalf("PopCtxWithTrace panicked: %v", p)
			}
			if err != nil || job == nil {
				t.Fatalf("PopCtxWithTrace = %v, %v; want the pushed job", job, err)
			}
			if n := out.Count("WARN", "velocity/queue: job type does not implement Identifiable"); n != 1 {
				t.Errorf("fallback WARN lines = %d, want 1:\n%s", n, out.String())
			}
		})
	}
}
