package async

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// findPanicEntry returns the first captured entry whose msg matches
// "async: panic recovered". Tests poll because the recovery log is emitted
// from a worker goroutine and may not be visible immediately.
func findPanicEntry(t *testing.T, cap *captureLogger) capturedEntry {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range cap.snapshot() {
			if e.msg == "async: panic recovered" {
				return e
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no panic recovered log captured; got %+v", cap.snapshot())
	return capturedEntry{}
}

// kvMap converts the slog-style kv pairs in a captured entry to a map keyed by
// the string key. Values are returned as `any` so callers can stringify.
func kvMap(t *testing.T, e capturedEntry) map[string]any {
	t.Helper()
	if len(e.kvs)%2 != 0 {
		t.Fatalf("odd number of kv pairs: %+v", e.kvs)
	}
	m := make(map[string]any, len(e.kvs)/2)
	for i := 0; i+1 < len(e.kvs); i += 2 {
		k, ok := e.kvs[i].(string)
		if !ok {
			t.Fatalf("non-string key at index %d: %+v", i, e.kvs[i])
		}
		m[k] = e.kvs[i+1]
	}
	return m
}

// assertStackContains verifies the "stack" field is a non-empty string that
// names the caller's test function. That proves debug.Stack() captured the
// real panicking goroutine's frames, not just the line in handlePanic.
func assertStackContains(t *testing.T, e capturedEntry, marker, callerFunc string) {
	t.Helper()
	m := kvMap(t, e)
	panicVal, ok := m["panic"]
	if !ok {
		t.Fatalf("entry missing 'panic' key: %+v", e.kvs)
	}
	if got := fmt.Sprintf("%v", panicVal); !strings.Contains(got, marker) {
		t.Fatalf("panic field %q does not contain marker %q", got, marker)
	}
	stack, ok := m["stack"].(string)
	if !ok {
		t.Fatalf("entry missing 'stack' key (or wrong type): %+v", e.kvs)
	}
	if stack == "" {
		t.Fatalf("stack field is empty")
	}
	if !strings.Contains(stack, callerFunc) {
		t.Fatalf("stack does not name caller %q (expected real goroutine frames):\n%s", callerFunc, stack)
	}
}

func TestPanicLogsStack_Run(t *testing.T) {
	cap := withLogger(t)

	r := Run(func() int {
		panic("test-panic-marker-Run")
	})
	_, err := r.Get()
	if err == nil {
		t.Fatal("expected error from Run after panic")
	}

	e := findPanicEntry(t, cap)
	assertStackContains(t, e, "test-panic-marker-Run", "TestPanicLogsStack_Run")
}

func TestPanicLogsStack_RunWithTimeout(t *testing.T) {
	cap := withLogger(t)

	r := RunWithTimeout(time.Second, func() int {
		panic("test-panic-marker-RWT")
	})
	// Guard with a deadline so a regression in panic-forwarding manifests
	// as a fast fatal, not a 60s test-suite hang.
	_, _ = getWithDeadline(t, r, 2*time.Second)

	e := findPanicEntry(t, cap)
	assertStackContains(t, e, "test-panic-marker-RWT", "TestPanicLogsStack_RunWithTimeout")
}

func TestPanicLogsStack_RunWithContext(t *testing.T) {
	cap := withLogger(t)

	r := RunWithContext(context.Background(), func() int {
		panic("test-panic-marker-RWC")
	})
	// Guard with a deadline; pre-fix this call hung forever because the
	// inner goroutine recovered the panic but did not forward it.
	_, _ = getWithDeadline(t, r, 2*time.Second)

	e := findPanicEntry(t, cap)
	assertStackContains(t, e, "test-panic-marker-RWC", "TestPanicLogsStack_RunWithContext")
}

func TestPanicLogsStack_Go(t *testing.T) {
	cap := withLogger(t)
	done := make(chan struct{})

	Go(func() {
		defer close(done)
		panic("test-panic-marker-Go")
	})
	<-done

	e := findPanicEntry(t, cap)
	assertStackContains(t, e, "test-panic-marker-Go", "TestPanicLogsStack_Go")
}

func TestPanicLogsStack_GoCtx(t *testing.T) {
	cap := withLogger(t)
	done := make(chan struct{})

	GoCtx(context.Background(), func(ctx context.Context) {
		defer close(done)
		panic("test-panic-marker-GoCtx")
	})
	<-done

	e := findPanicEntry(t, cap)
	assertStackContains(t, e, "test-panic-marker-GoCtx", "TestPanicLogsStack_GoCtx")
}

func TestPanicLogsStack_GoWithRecover_NilFallback(t *testing.T) {
	// nil recoverFn means GoWithRecover falls back to the package handler,
	// which is the path the user cares about ("fire and forget, no recover").
	cap := withLogger(t)
	done := make(chan struct{})

	GoWithRecover(func() {
		defer close(done)
		panic("test-panic-marker-GWR")
	}, nil)
	<-done

	e := findPanicEntry(t, cap)
	assertStackContains(t, e, "test-panic-marker-GWR", "TestPanicLogsStack_GoWithRecover_NilFallback")
}

func TestPanicLogsStack_GoWithRecoverE_NilFallback(t *testing.T) {
	cap := withLogger(t)
	done := make(chan struct{})

	GoWithRecoverE(func() {
		defer close(done)
		panic("test-panic-marker-GWRE")
	}, nil)
	<-done

	e := findPanicEntry(t, cap)
	assertStackContains(t, e, "test-panic-marker-GWRE", "TestPanicLogsStack_GoWithRecoverE_NilFallback")
}

func TestPanicLogsStack_GoForEach(t *testing.T) {
	cap := withLogger(t)

	GoForEach([]int{1}, 1, func(int) {
		panic("test-panic-marker-GFE")
	})

	e := findPanicEntry(t, cap)
	assertStackContains(t, e, "test-panic-marker-GFE", "TestPanicLogsStack_GoForEach")
}

func TestPanicLogsStack_ForEach(t *testing.T) {
	cap := withLogger(t)

	ForEach([]int{1}, 1, func(int) {
		panic("test-panic-marker-FE")
	})

	e := findPanicEntry(t, cap)
	assertStackContains(t, e, "test-panic-marker-FE", "TestPanicLogsStack_ForEach")
}

func TestPanicLogsStack_TryForEach(t *testing.T) {
	cap := withLogger(t)

	errs := TryForEach([]int{1}, 1, func(int) error {
		panic("test-panic-marker-TFE")
	})
	if len(errs) != 1 || errs[0] == nil {
		t.Fatalf("expected one non-nil error, got %+v", errs)
	}

	e := findPanicEntry(t, cap)
	assertStackContains(t, e, "test-panic-marker-TFE", "TestPanicLogsStack_TryForEach")
}

// TestPanicLogsStack_GoWithLogger_NameAndStack pins the named variant: it
// must carry BOTH the "name" field and the "stack" field, and the stack
// must name the caller test function.
func TestPanicLogsStack_GoWithLogger_NameAndStack(t *testing.T) {
	cap := &captureLogger{}
	done := make(chan struct{})

	GoWithLogger(cap, "worker-stack", func() {
		defer close(done)
		panic("test-panic-marker-GWL")
	})
	<-done

	e := findPanicEntry(t, cap)
	m := kvMap(t, e)
	name, ok := m["name"].(string)
	if !ok || name != "worker-stack" {
		t.Fatalf("expected name=worker-stack, got %v", m["name"])
	}
	assertStackContains(t, e, "test-panic-marker-GWL", "TestPanicLogsStack_GoWithLogger_NameAndStack")
}

// TestPanicLogsStack_StackOnlyOncePerRecovery is a guardrail: callers must
// not see duplicate stack fields. The kv slice is a flat list, and a buggy
// helper could append "stack" twice. We assert exactly one occurrence.
func TestPanicLogsStack_StackOnlyOncePerRecovery(t *testing.T) {
	cap := withLogger(t)

	r := Run(func() int {
		panic("test-panic-marker-Once")
	})
	_, _ = r.Get()

	e := findPanicEntry(t, cap)
	stackKeys := 0
	for i := 0; i+1 < len(e.kvs); i += 2 {
		if k, ok := e.kvs[i].(string); ok && k == "stack" {
			stackKeys++
		}
	}
	if stackKeys != 1 {
		t.Fatalf("expected exactly one 'stack' key, got %d in %+v", stackKeys, e.kvs)
	}
}
