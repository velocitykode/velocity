package async

import (
	"context"
	"sync"
	"testing"
	"time"
)

// captureLogger records every Error() call so tests can assert that the
// async package logged what we expected. Shared across helper tests.
type captureLogger struct {
	mu      sync.Mutex
	entries []capturedEntry
}

type capturedEntry struct {
	msg string
	kvs []any
}

func (c *captureLogger) Error(msg string, kvs ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]any, len(kvs))
	copy(cp, kvs)
	c.entries = append(c.entries, capturedEntry{msg: msg, kvs: cp})
}

func (c *captureLogger) snapshot() []capturedEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]capturedEntry, len(c.entries))
	copy(cp, c.entries)
	return cp
}

// withLogger swaps the package logger for the duration of the test and
// restores the previous one.
func withLogger(t *testing.T) *captureLogger {
	t.Helper()
	prev := getLogger()
	cap := &captureLogger{}
	SetLogger(cap)
	t.Cleanup(func() { SetLogger(prev) })
	return cap
}

// ---------- Item 8: GoCtx ----------

func TestGoCtx_FnCompletesBeforeCancel(t *testing.T) {
	ran := make(chan struct{})
	GoCtx(context.Background(), func(ctx context.Context) {
		close(ran)
	})
	select {
	case <-ran:
	case <-time.After(time.Second):
		t.Fatal("fn never executed")
	}
}

func TestGoCtx_LogsOnContextCancel(t *testing.T) {
	cap := withLogger(t)
	ctx, cancel := context.WithCancel(context.Background())

	fnExited := make(chan struct{})
	GoCtx(ctx, func(ctx context.Context) {
		<-ctx.Done()
		close(fnExited)
	})

	cancel()

	select {
	case <-fnExited:
	case <-time.After(time.Second):
		t.Fatal("fn never exited after cancel")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, e := range cap.snapshot() {
			if e.msg == "async: GoCtx context done" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected GoCtx context-done log, got: %+v", cap.snapshot())
}

func TestGoCtx_PanicRecovered(t *testing.T) {
	cap := withLogger(t)
	done := make(chan struct{})
	GoCtx(context.Background(), func(ctx context.Context) {
		defer close(done)
		panic("boom")
	})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("fn never ran")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, e := range cap.snapshot() {
			if e.msg == "async: panic recovered" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected panic log; got: %+v", cap.snapshot())
}

func TestGoCtx_NilContextDefaultsToBackground(t *testing.T) {
	done := make(chan struct{})
	GoCtx(nil, func(ctx context.Context) {
		if ctx == nil {
			t.Error("ctx should be non-nil inside fn")
		}
		close(done)
	})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("fn never executed under nil ctx")
	}
}

func TestGoCtx_TableDriven(t *testing.T) {
	tests := []struct {
		name      string
		setupCtx  func() (context.Context, context.CancelFunc)
		fn        func(ctx context.Context, signal chan struct{})
		expectLog bool
	}{
		{
			name: "fn returns before cancel",
			setupCtx: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			fn: func(ctx context.Context, signal chan struct{}) {
				close(signal)
			},
			expectLog: false,
		},
		{
			name: "ctx canceled while fn running",
			setupCtx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel() // pre-canceled
				return ctx, func() {}
			},
			fn: func(ctx context.Context, signal chan struct{}) {
				<-ctx.Done()
				close(signal)
			},
			expectLog: true,
		},
		{
			name: "ctx with deadline expires",
			setupCtx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 30*time.Millisecond)
			},
			fn: func(ctx context.Context, signal chan struct{}) {
				<-ctx.Done()
				close(signal)
			},
			expectLog: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cap := withLogger(t)
			ctx, cancel := tt.setupCtx()
			defer cancel()
			signal := make(chan struct{})
			GoCtx(ctx, func(c context.Context) { tt.fn(c, signal) })
			select {
			case <-signal:
			case <-time.After(time.Second):
				t.Fatal("fn never signaled")
			}
			deadline := time.Now().Add(500 * time.Millisecond)
			sawLog := false
			for time.Now().Before(deadline) {
				for _, e := range cap.snapshot() {
					if e.msg == "async: GoCtx context done" {
						sawLog = true
						break
					}
				}
				if sawLog {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if sawLog != tt.expectLog {
				t.Fatalf("expectLog=%v, sawLog=%v, entries=%+v",
					tt.expectLog, sawLog, cap.snapshot())
			}
		})
	}
}

// ---------- Item 9: GoWithRecover nil guard + GoWithLogger ----------

func TestGoWithRecover_NilRecoverFnFallsBackToHandler(t *testing.T) {
	cap := withLogger(t)

	// Nil recoverFn used to nil-panic the goroutine; now it should route
	// through handlePanic. The test passes if (a) we don't crash and (b)
	// the package logger sees the panic.
	GoWithRecover(func() {
		panic("nil-guarded boom")
	}, nil)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, e := range cap.snapshot() {
			if e.msg == "async: panic recovered" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected handlePanic log; got: %+v", cap.snapshot())
}

func TestGoWithRecover_CustomRecoverFnStillCalled(t *testing.T) {
	got := make(chan any, 1)
	GoWithRecover(func() { panic("x") }, func(p any) { got <- p })
	select {
	case p := <-got:
		if p != "x" {
			t.Fatalf("got %v", p)
		}
	case <-time.After(time.Second):
		t.Fatal("recoverFn never called")
	}
}

func TestGoWithRecover_PanicInsideRecoverFnDoesNotCrash(t *testing.T) {
	cap := withLogger(t)
	GoWithRecover(func() { panic("outer") }, func(p any) {
		panic("inner")
	})
	// Test passes if we don't crash; we expect at least one log entry from
	// the outer-recover catching the inner panic.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(cap.snapshot()) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected at least one panic log")
}

func TestGoWithLogger_RoutesPanicWithName(t *testing.T) {
	cap := &captureLogger{}
	done := make(chan struct{})

	GoWithLogger(cap, "worker-7", func() {
		defer close(done)
		panic("scoped panic")
	})

	<-done
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, e := range cap.snapshot() {
			if e.msg == "async: panic recovered" {
				foundName := false
				for i := 0; i+1 < len(e.kvs); i += 2 {
					if k, ok := e.kvs[i].(string); ok && k == "name" {
						if v, ok := e.kvs[i+1].(string); ok && v == "worker-7" {
							foundName = true
						}
					}
				}
				if !foundName {
					t.Fatalf("expected name=worker-7 in kvs, got %+v", e.kvs)
				}
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected panic log; got %+v", cap.snapshot())
}

func TestGoWithLogger_NilLoggerFallsBackToPackageLogger(t *testing.T) {
	cap := withLogger(t)
	GoWithLogger(nil, "fallback", func() { panic("p") })
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(cap.snapshot()) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected package logger to receive panic")
}
