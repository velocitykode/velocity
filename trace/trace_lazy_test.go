package trace

import (
	"context"
	"io"
	"regexp"
	"sync"
	"testing"
)

// countingReader counts entropy reads so tests can assert laziness.
type countingReader struct {
	mu    sync.Mutex
	reads int
	inner io.Reader
}

func (c *countingReader) Read(p []byte) (int, error) {
	c.mu.Lock()
	c.reads++
	c.mu.Unlock()
	return c.inner.Read(p)
}

func (c *countingReader) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads
}

func TestStartTraceLazy_NoReadNoGeneration(t *testing.T) {
	counter := &countingReader{inner: randReader}
	withRandReader(t, counter)

	_, _ = StartTraceLazy(context.Background())

	if got := counter.count(); got != 0 {
		t.Fatalf("expected no entropy reads before first ID access, got %d", got)
	}
}

func TestStartTraceLazy_GettersMaterializeAndAgree(t *testing.T) {
	ctx, lazy := StartTraceLazy(context.Background())

	traceID := GetTraceID(ctx)
	spanID := GetSpanID(ctx)

	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(traceID) {
		t.Errorf("trace ID %q is not 32 hex chars", traceID)
	}
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(spanID) {
		t.Errorf("span ID %q is not 16 hex chars", spanID)
	}

	// The holder and every subsequent read observe the same pair.
	holderTrace, holderSpan := lazy.IDs()
	if holderTrace != traceID || holderSpan != spanID {
		t.Errorf("holder IDs (%q, %q) differ from context reads (%q, %q)",
			holderTrace, holderSpan, traceID, spanID)
	}
	if again := GetTraceID(ctx); again != traceID {
		t.Errorf("second GetTraceID read %q, want %q", again, traceID)
	}
}

func TestStartTraceLazy_HolderFirstMatchesContextReads(t *testing.T) {
	ctx, lazy := StartTraceLazy(context.Background())

	holderTrace, holderSpan := lazy.IDs()
	if got := GetTraceID(ctx); got != holderTrace {
		t.Errorf("GetTraceID = %q, want holder value %q", got, holderTrace)
	}
	if got := GetSpanID(ctx); got != holderSpan {
		t.Errorf("GetSpanID = %q, want holder value %q", got, holderSpan)
	}
}

func TestStartTraceLazy_DelegatesOtherKeys(t *testing.T) {
	base := WithFullContext(context.Background(), "t-old", "s-old", "p-old")
	ctx, _ := StartTraceLazy(base)

	// Trace and span keys are answered by the lazy layer (fresh IDs);
	// every other key falls through to the wrapped context.
	if got := GetParentID(ctx); got != "p-old" {
		t.Errorf("GetParentID = %q, want %q", got, "p-old")
	}
	if got := GetTraceID(ctx); got == "t-old" {
		t.Error("GetTraceID returned the shadowed outer value, want a fresh lazy ID")
	}
}

func TestStartTraceLazy_ConcurrentReadsAgree(t *testing.T) {
	ctx, _ := StartTraceLazy(context.Background())

	const readers = 32
	ids := make([]string, readers)
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			ids[slot] = GetTraceID(ctx)
		}(i)
	}
	wg.Wait()

	for i := 1; i < readers; i++ {
		if ids[i] != ids[0] {
			t.Fatalf("concurrent read %d got %q, want %q", i, ids[i], ids[0])
		}
	}
}

func TestStartTraceLazy_WithSpanLayersOnTop(t *testing.T) {
	ctx, _ := StartTraceLazy(context.Background())
	originalSpan := GetSpanID(ctx)

	child, newSpan := WithNewSpan(ctx)

	if got := GetSpanID(child); got != newSpan {
		t.Errorf("child span = %q, want %q", got, newSpan)
	}
	if got := GetParentID(child); got != originalSpan {
		t.Errorf("child parent = %q, want original span %q", got, originalSpan)
	}
	if GetTraceID(child) != GetTraceID(ctx) {
		t.Error("trace ID changed across WithNewSpan")
	}
}

func TestStartTraceLazy_RandFailureFallsBack(t *testing.T) {
	withRandReader(t, failingReader{})

	ctx, _ := StartTraceLazy(context.Background())

	traceID := GetTraceID(ctx)
	spanID := GetSpanID(ctx)
	if !regexp.MustCompile(`^velocity_trace_norand_\d+_\d+$`).MatchString(traceID) {
		t.Errorf("trace ID %q does not match fallback shape", traceID)
	}
	if !regexp.MustCompile(`^velocity_span_norand_\d+_\d+$`).MatchString(spanID) {
		t.Errorf("span ID %q does not match fallback shape", spanID)
	}
}
