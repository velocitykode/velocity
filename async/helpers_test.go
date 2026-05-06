package async

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/panicerr"
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
	// GoCtx documents nil-tolerance: a nil ctx is normalized to
	// context.Background() inside the helper. This test exercises that
	// contract intentionally, so the SA1012 lint is suppressed here.
	//lint:ignore SA1012 deliberately testing nil-ctx normalization
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

// ---------- Item 10: GoForEach + TryForEach ----------

func TestGoForEach_ReturnsImmediatelyAndRunsAllItems(t *testing.T) {
	items := []int{1, 2, 3, 4, 5}
	var sum atomic.Int64
	var done atomic.Int32

	start := time.Now()
	GoForEach(items, 2, func(i int) {
		// orchestration: sleep is test input. work that outlasts the
		// "did GoForEach return immediately?" assertion below.
		time.Sleep(50 * time.Millisecond)
		sum.Add(int64(i))
		done.Add(1)
	})
	returned := time.Since(start)
	if returned > 30*time.Millisecond {
		t.Fatalf("GoForEach should return immediately, took %v", returned)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if done.Load() == int32(len(items)) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if done.Load() != int32(len(items)) {
		t.Fatalf("expected %d done, got %d", len(items), done.Load())
	}
	if sum.Load() != 15 {
		t.Fatalf("sum mismatch: got %d, want 15", sum.Load())
	}
}

func TestGoForEach_RespectsConcurrencyLimit(t *testing.T) {
	items := []int{1, 2, 3, 4, 5, 6}
	var active atomic.Int32
	var maxActive atomic.Int32
	done := make(chan struct{}, len(items))

	GoForEach(items, 2, func(int) {
		cur := active.Add(1)
		for {
			old := maxActive.Load()
			if cur <= old || maxActive.CompareAndSwap(old, cur) {
				break
			}
		}
		// orchestration: sleep is test input. holds workers active so the
		// concurrency-limit assertion has a window to observe.
		time.Sleep(40 * time.Millisecond)
		active.Add(-1)
		done <- struct{}{}
	})
	for i := 0; i < len(items); i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("worker stalled")
		}
	}
	if got := maxActive.Load(); got > 2 {
		t.Fatalf("concurrency limit violated: max active = %d", got)
	}
}

func TestGoForEach_EmptySliceIsNoop(t *testing.T) {
	GoForEach([]int{}, 2, func(int) { t.Fatal("should not be called") })
	GoForEach[int](nil, 2, func(int) { t.Fatal("should not be called") })
}

func TestGoForEach_CallerCanMutateInputAfterReturn(t *testing.T) {
	items := []int{1, 2, 3}
	var seen sync.Map
	var done atomic.Int32

	GoForEach(items, 1, func(i int) {
		seen.Store(i, true)
		done.Add(1)
	})
	// Mutate the original slice immediately. GoForEach must have snapshotted.
	for i := range items {
		items[i] = -1
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if done.Load() == 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, want := range []int{1, 2, 3} {
		if _, ok := seen.Load(want); !ok {
			t.Fatalf("missing %d in seen set", want)
		}
	}
}

func TestGoForEach_PanicInItemRecovered(t *testing.T) {
	cap := withLogger(t)
	items := []int{1, 2, 3}
	var done atomic.Int32

	GoForEach(items, 2, func(i int) {
		defer done.Add(1)
		if i == 2 {
			panic("item-2 explosion")
		}
	})

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if done.Load() == 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if done.Load() != 3 {
		t.Fatalf("expected all items to attempt; got %d (logs=%+v)", done.Load(), cap.snapshot())
	}
	// Panic must have been logged.
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, e := range cap.snapshot() {
			if e.msg == "async: panic recovered" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected panic log; got %+v", cap.snapshot())
}

func TestTryForEach_CollectsErrorsInOrder(t *testing.T) {
	items := []string{"ok", "boom", "ok", "boom", "ok"}
	errs := TryForEach(items, 3, func(s string) error {
		if s == "boom" {
			return fmt.Errorf("err for %s", s)
		}
		return nil
	})
	if len(errs) != len(items) {
		t.Fatalf("len(errs)=%d, want %d", len(errs), len(items))
	}
	for i, want := range []bool{false, true, false, true, false} {
		got := errs[i] != nil
		if got != want {
			t.Errorf("errs[%d]: got err=%v, want err=%v (%v)", i, got, want, errs[i])
		}
	}
}

func TestTryForEach_AllSucceedReturnsAllNil(t *testing.T) {
	items := []int{1, 2, 3, 4}
	errs := TryForEach(items, 2, func(int) error { return nil })
	if len(errs) != 4 {
		t.Fatalf("unexpected length: %d", len(errs))
	}
	for i, e := range errs {
		if e != nil {
			t.Errorf("errs[%d] = %v, want nil", i, e)
		}
	}
}

func TestTryForEach_EmptySliceReturnsEmptySlice(t *testing.T) {
	errs := TryForEach([]int{}, 1, func(int) error { return errors.New("never") })
	if errs == nil {
		t.Fatal("errs should be non-nil")
	}
	if len(errs) != 0 {
		t.Fatalf("expected len 0, got %d", len(errs))
	}
}

func TestTryForEach_PanicSurfacedAsError(t *testing.T) {
	cap := withLogger(t)
	items := []int{0, 1, 2}
	errs := TryForEach(items, 2, func(i int) error {
		if i == 1 {
			panic("panic at index 1")
		}
		return nil
	})
	if errs[0] != nil || errs[2] != nil {
		t.Fatalf("non-panic items should have nil errs, got %v / %v", errs[0], errs[2])
	}
	if errs[1] == nil {
		t.Fatalf("expected panic surfaced as error at index 1; logs=%+v", cap.snapshot())
	}
	if errs[1].Error() == "" {
		t.Fatalf("error message should be non-empty: %v", errs[1])
	}
}

func TestTryForEach_RespectsConcurrency(t *testing.T) {
	items := []int{1, 2, 3, 4, 5, 6}
	var active atomic.Int32
	var maxActive atomic.Int32
	_ = TryForEach(items, 2, func(int) error {
		cur := active.Add(1)
		for {
			old := maxActive.Load()
			if cur <= old || maxActive.CompareAndSwap(old, cur) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		active.Add(-1)
		return nil
	})
	if got := maxActive.Load(); got > 2 {
		t.Fatalf("concurrency violated: max=%d", got)
	}
}

func TestGoForEach_TryForEach_ConcurrentInvocations(t *testing.T) {
	withLogger(t) // silence

	const calls = 8
	var wg sync.WaitGroup
	var collected sync.Map

	for i := 0; i < calls; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			items := []int{i*10 + 1, i*10 + 2, i*10 + 3}
			errs := TryForEach(items, 2, func(v int) error {
				collected.Store(v, true)
				return nil
			})
			if len(errs) != 3 {
				t.Errorf("invocation %d: len=%d", i, len(errs))
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			items := []int{i*100 + 1, i*100 + 2}
			done := make(chan struct{}, len(items))
			GoForEach(items, 2, func(v int) {
				collected.Store(-v, true)
				done <- struct{}{}
			})
			for range items {
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Errorf("invocation %d: GoForEach worker stalled", i)
					return
				}
			}
		}()
	}
	wg.Wait()

	var saw []int
	collected.Range(func(k, _ any) bool {
		saw = append(saw, k.(int))
		return true
	})
	sort.Ints(saw)
	if len(saw) < calls*5 {
		t.Fatalf("expected at least %d unique items, saw %d (%v)", calls*5, len(saw), saw)
	}
}

// ---------- Item 11: typed PanicError + GoWithRecoverE ----------

func TestPanicError_WrapsErrorValue(t *testing.T) {
	sentinel := errors.New("upstream failed")
	pe := panicerr.New(sentinel)
	if pe == nil {
		t.Fatal("New should not return nil for non-nil value")
	}
	if !errors.Is(pe, sentinel) {
		t.Fatalf("errors.Is should walk the chain to sentinel; got: %v", pe)
	}
	if pe.Recovered() != sentinel {
		t.Fatalf("Recovered() mismatch")
	}
	if got := pe.Error(); got != "panic: upstream failed" {
		t.Fatalf("Error()=%q", got)
	}
}

func TestPanicError_WrapsStringValue(t *testing.T) {
	pe := panicerr.New("plain string panic")
	if pe == nil {
		t.Fatal("expected non-nil")
	}
	if pe.Recovered() != "plain string panic" {
		t.Fatalf("Recovered=%v", pe.Recovered())
	}
	if pe.Unwrap() != nil {
		t.Fatal("Unwrap should be nil for non-error panic value")
	}
}

func TestPanicError_NilValueReturnsNil(t *testing.T) {
	if pe := panicerr.New(nil); pe != nil {
		t.Fatalf("expected nil from New(nil); got %v", pe)
	}
	if e := panicerr.FromRecovered(nil); e != nil {
		t.Fatalf("expected nil from FromRecovered(nil); got %v", e)
	}
}

func TestPanicError_AsTyped(t *testing.T) {
	wrapped := fmt.Errorf("outer: %w", panicerr.New("inner panic"))
	pe := panicerr.AsTyped(wrapped)
	if pe == nil {
		t.Fatal("expected to find *PanicError via errors.As")
	}
	if pe.Recovered() != "inner panic" {
		t.Fatalf("recovered=%v", pe.Recovered())
	}
}

func TestGoWithRecoverE_TypedPanic(t *testing.T) {
	got := make(chan *PanicError, 1)
	GoWithRecoverE(func() { panic(errors.New("typed boom")) }, func(p *PanicError) {
		got <- p
	})
	select {
	case p := <-got:
		if p == nil {
			t.Fatal("nil *PanicError")
		}
		if p.Recovered() == nil {
			t.Fatal("expected non-nil recovered")
		}
	case <-time.After(time.Second):
		t.Fatal("recoverFn never called")
	}
}

func TestGoWithRecoverE_NilRecoverFnFallsBack(t *testing.T) {
	cap := withLogger(t)
	GoWithRecoverE(func() { panic("e nil") }, nil)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, e := range cap.snapshot() {
			if e.msg == "async: panic recovered" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected panic log; got %+v", cap.snapshot())
}

func TestAsyncFromRecovered_ReExportMatchesInternal(t *testing.T) {
	a := FromRecovered("xyz")
	b := panicerr.FromRecovered("xyz")
	if a == nil || b == nil {
		t.Fatal("both should be non-nil")
	}
	if a.Error() != b.Error() {
		t.Fatalf("re-export mismatch: %q vs %q", a.Error(), b.Error())
	}
}

func TestPanicError_NilReceiverSafe(t *testing.T) {
	var pe *PanicError
	if pe.Error() != "" {
		t.Fatal("nil receiver Error() should return empty string")
	}
	if pe.Recovered() != nil {
		t.Fatal("nil receiver Recovered() should be nil")
	}
	if pe.Unwrap() != nil {
		t.Fatal("nil receiver Unwrap() should be nil")
	}
}

// ---------- Item 12: GetLogger + SetPanicHook ----------

func TestGetLogger_ReturnsCurrentLogger(t *testing.T) {
	prev := GetLogger()
	defer SetLogger(prev)

	cap := &captureLogger{}
	SetLogger(cap)
	if GetLogger() != cap {
		t.Fatalf("GetLogger returned %T, want *captureLogger", GetLogger())
	}
	GetLogger().Error("hi")
	if entries := cap.snapshot(); len(entries) != 1 || entries[0].msg != "hi" {
		t.Fatalf("expected 1 entry msg=hi, got %+v", entries)
	}
}

func TestGetLogger_ReadsLatestAfterSetLogger(t *testing.T) {
	prev := GetLogger()
	defer SetLogger(prev)

	a := &captureLogger{}
	b := &captureLogger{}

	SetLogger(a)
	if got := GetLogger(); got != a {
		t.Fatalf("after SetLogger(a), got %T", got)
	}
	SetLogger(b)
	if got := GetLogger(); got != b {
		t.Fatalf("after SetLogger(b), got %T", got)
	}
}

func TestGetLogger_ConcurrentReadsAndWrites(t *testing.T) {
	prev := GetLogger()
	defer SetLogger(prev)

	const N = 50
	var wg sync.WaitGroup
	wg.Add(2 * N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			SetLogger(&captureLogger{})
		}()
		go func() {
			defer wg.Done()
			_ = GetLogger()
		}()
	}
	wg.Wait()
}

func TestSetPanicHook_FiresOnEveryHelperPanic(t *testing.T) {
	withLogger(t) // silence the std logger

	var calls atomic.Int32
	SetPanicHook(func(any) { calls.Add(1) })
	t.Cleanup(func() { SetPanicHook(nil) })

	// Each helper panics in turn; we tally 1 call per helper.
	helpers := []func(){
		func() {
			done := make(chan struct{})
			Go(func() { defer close(done); panic("a") })
			<-done
		},
		func() {
			done := make(chan struct{})
			GoWithRecover(func() { defer close(done); panic("b") }, nil)
			<-done
		},
		func() {
			done := make(chan struct{})
			GoWithRecover(func() { defer close(done); panic("b2") }, func(any) {})
			<-done
		},
		func() {
			done := make(chan struct{})
			GoWithRecoverE(func() { defer close(done); panic("c") }, nil)
			<-done
		},
		func() {
			done := make(chan struct{})
			GoWithRecoverE(func() { defer close(done); panic("c2") }, func(*PanicError) {})
			<-done
		},
		func() {
			done := make(chan struct{})
			GoWithLogger(nil, "h", func() { defer close(done); panic("d") })
			<-done
		},
		func() {
			TryForEach([]int{0}, 1, func(int) error { panic("e") })
		},
	}
	for _, h := range helpers {
		h()
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() >= int32(len(helpers)) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := calls.Load(); got < int32(len(helpers)) {
		t.Fatalf("hook fired %d times, want >= %d", got, len(helpers))
	}
}

func TestSetPanicHook_ClearedByNil(t *testing.T) {
	withLogger(t)

	var calls atomic.Int32
	SetPanicHook(func(any) { calls.Add(1) })
	SetPanicHook(nil) // clear

	done := make(chan struct{})
	Go(func() { defer close(done); panic("after-clear") })
	<-done
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatalf("hook should have been cleared, fired %d times", calls.Load())
	}
}

func TestSetPanicHook_PanicInsideHookSwallowed(t *testing.T) {
	withLogger(t)

	SetPanicHook(func(any) { panic("hook itself panics") })
	t.Cleanup(func() { SetPanicHook(nil) })

	done := make(chan struct{})
	Go(func() { defer close(done); panic("outer") })
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Go() goroutine never closed done")
	}
}

func TestSetPanicHook_ConcurrentSetAndFire(t *testing.T) {
	withLogger(t)

	var fires atomic.Int32
	t.Cleanup(func() { SetPanicHook(nil) })

	const N = 30
	var wg sync.WaitGroup
	wg.Add(2 * N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			SetPanicHook(func(any) { fires.Add(1) })
		}()
		go func() {
			defer wg.Done()
			done := make(chan struct{})
			Go(func() { defer close(done); panic("racer") })
			<-done
		}()
	}
	wg.Wait()
	// We can't assert exact fire count under the race because some panics
	// land before SetPanicHook installs; we just need to ensure no race
	// detector failures and no crashes. Test passes by completing.
}
