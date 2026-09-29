package redis

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/queue"
	"github.com/velocitykode/velocity/trace"
)

// A redis driver used standalone, with no logger, writes its insecure-host
// startup warnings through the one framework fallback and nothing through
// the standard library log package or slog.Default.
func TestWarnIfInsecure_WithoutLoggerWritesThroughTheFallback(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	stdlib := fallbacklogtest.CaptureStdlib(t)

	warnIfInsecure(nil, "10.0.0.7", "", false)

	for _, msg := range []string{
		"velocity/queue: redis driver connecting to non-loopback host without TLS",
		"velocity/queue: redis driver connecting to non-loopback host without a password",
	} {
		if got := fallback.Count("WARN", msg); got != 1 {
			t.Errorf("fallback lines %q = %d, want 1: %q", msg, got, fallback.String())
		}
	}
	if out := stdlib.String(); out != "" {
		t.Errorf("stdlib log / slog.Default got %q, want nothing", out)
	}
}

// fallbackProbeJob is a registered job type no other test pops, so the
// driver's once-per-type advisory fires for it on the first pop.
type fallbackProbeJob struct {
	ID string `json:"id"`
}

func (*fallbackProbeJob) Handle() error { return nil }
func (*fallbackProbeJob) Failed(error)  {}

func init() {
	queue.RegisterJob(func(data []byte) (*fallbackProbeJob, error) {
		var job fallbackProbeJob
		if err := json.Unmarshal(data, &job); err != nil {
			return nil, err
		}
		return &job, nil
	})
}

// pushAndPop pushes a fallbackProbeJob through d and pops it back, the
// point the non-Identifiable advisory is written.
func pushAndPop(t *testing.T, d queue.Driver) {
	t.Helper()
	saveAndRestoreSigningState(t)
	queue.SetSigningKey(nil)
	const name = "fallback-probe"
	if err := d.PushCtx(context.Background(), &fallbackProbeJob{ID: "1"}, name); err != nil {
		t.Fatalf("PushCtx: %v", err)
	}
	if _, err := d.PopCtx(context.Background(), name); err != nil {
		t.Fatalf("PopCtx: %v", err)
	}
}

// The redis driver without a logger writes its advisory through the
// fallback.
func TestRedisDriver_WithoutLoggerWarnsThroughTheFallback(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	fallback := fallbacklogtest.Capture(t)

	d, err := NewRedisDriver(queue.RedisConfig{Host: mr.Host(), Port: mr.Port(), DB: "0"})
	if err != nil {
		t.Fatalf("NewRedisDriver: %v", err)
	}
	t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
	pushAndPop(t, d)

	if got := fallback.Count("WARN", "velocity/queue: job type does not implement Identifiable"); got != 1 {
		t.Errorf("fallback lines = %d, want 1: %q", got, fallback.String())
	}
}

// A logger in QueueConfig reaches the driver the registry builds: its
// construction-time warnings and its later advisories.
func TestNewQueue_HandsConfigLoggerToTheDriver(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	logs := &redisCaptureLogger{}

	d, err := queue.NewQueue(queue.QueueConfig{
		Driver: "redis",
		Redis:  queue.RedisConfig{Host: mr.Host(), Port: mr.Port(), DB: "0"},
		Logger: logs,
	})
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
	pushAndPop(t, d)

	if got := logs.countContaining("velocity/queue: job type does not implement Identifiable"); got != 1 {
		t.Errorf("config logger lines = %d, want 1", got)
	}
}

// The advisory for a popped job without an id carries the ids of the
// context it was popped under.
func TestRedisDriver_NonIdentifiableWarningCarriesThePopContext(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	fallback := fallbacklogtest.Capture(t)
	saveAndRestoreSigningState(t)
	queue.SetSigningKey(nil)

	d, err := NewRedisDriver(queue.RedisConfig{Host: mr.Host(), Port: mr.Port(), DB: "0"})
	if err != nil {
		t.Fatalf("NewRedisDriver: %v", err)
	}
	t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
	const name = "correlated-probe"
	if err := d.PushCtx(context.Background(), &fallbackProbeJob{ID: "1"}, name); err != nil {
		t.Fatalf("PushCtx: %v", err)
	}
	ctx := trace.WithRequestID(trace.WithTrace(context.Background(), "t4", "s4"), "r4")
	if _, err := d.PopCtx(ctx, name); err != nil {
		t.Fatalf("PopCtx: %v", err)
	}
	out := fallback.String()
	for _, want := range []string{"request_id=r4", "trace_id=t4", "span_id=s4"} {
		if !strings.Contains(out, want) {
			t.Errorf("advisory = %q, want %s", out, want)
		}
	}
}
