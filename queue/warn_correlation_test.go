package queue

import (
	"context"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/trace"
)

// pairLogger records the pairs, bound ones first, of every Warn.
type pairLogger struct {
	mu    *sync.Mutex
	lines *[][]any
	bound []any
}

func (l pairLogger) Debug(string, ...any) {}
func (l pairLogger) Info(string, ...any)  {}
func (l pairLogger) Error(string, ...any) {}
func (l pairLogger) Fatal(string, ...any) {}
func (l pairLogger) Warn(_ string, kvs ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	*l.lines = append(*l.lines, append(append([]any(nil), l.bound...), kvs...))
}
func (l pairLogger) With(kvs ...any) contract.Logger {
	return pairLogger{mu: l.mu, lines: l.lines, bound: append(append([]any(nil), l.bound...), kvs...)}
}

// The memory driver's warning for a job without an id, written while
// pushing it, carries the pushing request's ids.
func TestMemoryDriver_NonIdentifiableWarningCarriesThePushContext(t *testing.T) {
	var mu sync.Mutex
	var lines [][]any
	d := NewMemoryDriver()
	d.SetLogger(pairLogger{mu: &mu, lines: &lines})
	t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
	ctx := trace.WithRequestID(trace.WithTrace(context.Background(), "t3", "s3"), "r3")

	if err := d.PushCtx(ctx, fallbackProbeJob{}); err != nil {
		t.Fatalf("PushCtx: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 1 {
		t.Fatalf("warn lines = %d, want 1", len(lines))
	}
	fields := map[any]any{}
	for i := 0; i+1 < len(lines[0]); i += 2 {
		fields[lines[0][i]] = lines[0][i+1]
	}
	for key, want := range map[string]string{"request_id": "r3", "trace_id": "t3", "span_id": "s3"} {
		if got := fields[key]; got != want {
			t.Errorf("%s = %v, want %q (%v)", key, got, want, lines[0])
		}
	}
}
