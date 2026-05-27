package trace

import (
	"context"
	"errors"
	"io"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// failingReader always returns an error, simulating a sandboxed or
// chrooted environment where crypto/rand is unavailable.
type failingReader struct{}

func (failingReader) Read(p []byte) (int, error) {
	return 0, errors.New("entropy source unavailable")
}

// withRandReader swaps the package-level randReader for the duration of
// the test and restores it on cleanup. Also resets the one-time WARN
// guard so each test can independently observe the log path.
func withRandReader(t *testing.T, r io.Reader) {
	t.Helper()
	prev := randReader
	randReader = r
	prevOnce := randFallbackWarnOnce
	randFallbackWarnOnce = sync.Once{}
	t.Cleanup(func() {
		randReader = prev
		randFallbackWarnOnce = prevOnce
	})
}

func TestGenerateTraceID_RandFailureReturnsError(t *testing.T) {
	withRandReader(t, failingReader{})

	id, err := GenerateTraceID()
	if err == nil {
		t.Fatal("expected error when rand source fails, got nil")
	}
	if id != "" {
		t.Errorf("expected empty id on error, got %q", id)
	}
	// The historical bug: silently returning all-zero hex. Guard against
	// regression by asserting we never emit a 32-hex zero string.
	zeros := strings.Repeat("0", 32)
	if id == zeros {
		t.Fatal("regression: GenerateTraceID returned all-zero hex on rand failure")
	}
}

func TestGenerateSpanID_RandFailureReturnsError(t *testing.T) {
	withRandReader(t, failingReader{})

	id, err := GenerateSpanID()
	if err == nil {
		t.Fatal("expected error when rand source fails, got nil")
	}
	if id != "" {
		t.Errorf("expected empty id on error, got %q", id)
	}
	zeros := strings.Repeat("0", 16)
	if id == zeros {
		t.Fatal("regression: GenerateSpanID returned all-zero hex on rand failure")
	}
}

// fallbackTraceIDRe matches the documented per-call fallback shape:
// velocity_trace_norand_<processStartNs>_<counter> where both
// numeric segments are unbounded decimal digits.
var fallbackTraceIDRe = regexp.MustCompile(`^velocity_trace_norand_\d+_\d+$`)
var fallbackSpanIDRe = regexp.MustCompile(`^velocity_span_norand_\d+_\d+$`)

func TestMustGenerateTraceID_FallbackShape(t *testing.T) {
	withRandReader(t, failingReader{})

	id := MustGenerateTraceID()
	if !strings.HasPrefix(id, FallbackTraceIDPrefix) {
		t.Fatalf("expected prefix %q, got %q", FallbackTraceIDPrefix, id)
	}
	if !fallbackTraceIDRe.MatchString(id) {
		t.Fatalf("fallback trace id %q does not match %s", id, fallbackTraceIDRe)
	}
	// Must not be hex-shaped at the canonical length so any APM that
	// pattern-matches ^[0-9a-f]{32}$ filters it out.
	if len(id) == 32 && isHexOnly(id) {
		t.Errorf("fallback id must not be 32-hex; got %q", id)
	}
	if id == strings.Repeat("0", 32) {
		t.Errorf("fallback id must not be all-zero hex; got %q", id)
	}
}

func TestMustGenerateSpanID_FallbackShape(t *testing.T) {
	withRandReader(t, failingReader{})

	id := MustGenerateSpanID()
	if !strings.HasPrefix(id, FallbackSpanIDPrefix) {
		t.Fatalf("expected prefix %q, got %q", FallbackSpanIDPrefix, id)
	}
	if !fallbackSpanIDRe.MatchString(id) {
		t.Fatalf("fallback span id %q does not match %s", id, fallbackSpanIDRe)
	}
	if len(id) == 16 && isHexOnly(id) {
		t.Errorf("fallback id must not be 16-hex; got %q", id)
	}
	if id == strings.Repeat("0", 16) {
		t.Errorf("fallback id must not be all-zero hex; got %q", id)
	}
}

// TestMustGenerateTraceID_SequentialUniqueness exercises the core
// reviewer requirement: 1000 sequential fallback IDs must all be
// distinct so concurrent in-flight traces stay correlatable during an
// entropy outage.
func TestMustGenerateTraceID_SequentialUniqueness(t *testing.T) {
	withRandReader(t, failingReader{})

	const calls = 1000
	seen := make(map[string]struct{}, calls)
	for i := 0; i < calls; i++ {
		id := MustGenerateTraceID()
		if !fallbackTraceIDRe.MatchString(id) {
			t.Fatalf("call %d: id %q does not match fallback shape", i, id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("call %d: duplicate fallback trace id %q", i, id)
		}
		seen[id] = struct{}{}
	}
	if got := len(seen); got != calls {
		t.Fatalf("distinct count: got %d want %d", got, calls)
	}
}

func TestMustGenerateSpanID_SequentialUniqueness(t *testing.T) {
	withRandReader(t, failingReader{})

	const calls = 1000
	seen := make(map[string]struct{}, calls)
	for i := 0; i < calls; i++ {
		id := MustGenerateSpanID()
		if !fallbackSpanIDRe.MatchString(id) {
			t.Fatalf("call %d: id %q does not match fallback shape", i, id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("call %d: duplicate fallback span id %q", i, id)
		}
		seen[id] = struct{}{}
	}
	if got := len(seen); got != calls {
		t.Fatalf("distinct count: got %d want %d", got, calls)
	}
}

// TestMustGenerateTraceID_ConcurrentUniqueness fires 100 goroutines x
// 100 calls each (10,000 calls) under a dead rand source and asserts
// every fallback ID is distinct. atomic.Uint64 guarantees this; the
// test pins the contract.
func TestMustGenerateTraceID_ConcurrentUniqueness(t *testing.T) {
	withRandReader(t, failingReader{})

	const workers = 100
	const perWorker = 100
	const total = workers * perWorker

	var set sync.Map
	var dupCount int64
	var dupMu sync.Mutex

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				id := MustGenerateTraceID()
				if _, loaded := set.LoadOrStore(id, struct{}{}); loaded {
					dupMu.Lock()
					dupCount++
					dupMu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	if dupCount != 0 {
		t.Fatalf("%d duplicate fallback trace ids across %d concurrent calls", dupCount, total)
	}

	// Count distinct keys in the sync.Map and pin to total.
	var distinct int
	set.Range(func(_, _ any) bool {
		distinct++
		return true
	})
	if distinct != total {
		t.Fatalf("distinct count: got %d want %d", distinct, total)
	}
}

func TestMustGenerateSpanID_ConcurrentUniqueness(t *testing.T) {
	withRandReader(t, failingReader{})

	const workers = 100
	const perWorker = 100
	const total = workers * perWorker

	var set sync.Map
	var dupCount int64
	var dupMu sync.Mutex

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				id := MustGenerateSpanID()
				if _, loaded := set.LoadOrStore(id, struct{}{}); loaded {
					dupMu.Lock()
					dupCount++
					dupMu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	if dupCount != 0 {
		t.Fatalf("%d duplicate fallback span ids across %d concurrent calls", dupCount, total)
	}
	var distinct int
	set.Range(func(_, _ any) bool {
		distinct++
		return true
	})
	if distinct != total {
		t.Fatalf("distinct count: got %d want %d", distinct, total)
	}
}

// TestMustGenerate_TraceAndSpanNeverEqual asserts that within a single
// outage the fallback trace IDs and span IDs are never equal even
// though both use the same counter. Different prefixes are the only
// thing keeping them apart; this test pins that.
func TestMustGenerate_TraceAndSpanNeverEqual(t *testing.T) {
	withRandReader(t, failingReader{})

	seen := make(map[string]struct{}, 200)
	for i := 0; i < 100; i++ {
		tid := MustGenerateTraceID()
		sid := MustGenerateSpanID()
		if tid == sid {
			t.Fatalf("iter %d: trace id %q == span id %q", i, tid, sid)
		}
		if _, dup := seen[tid]; dup {
			t.Fatalf("iter %d: duplicate trace id %q", i, tid)
		}
		seen[tid] = struct{}{}
		if _, dup := seen[sid]; dup {
			t.Fatalf("iter %d: duplicate span id %q", i, sid)
		}
		seen[sid] = struct{}{}
	}
}

// retryingReader fails the first N reads then succeeds. Used to verify
// that the Must* helpers actually retry once before falling back.
type retryingReader struct {
	mu        sync.Mutex
	failsLeft int
}

func (r *retryingReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failsLeft > 0 {
		r.failsLeft--
		return 0, errors.New("transient failure")
	}
	for i := range p {
		p[i] = byte(i + 1)
	}
	return len(p), nil
}

func TestMustGenerateTraceID_RetriesOnceBeforeFallback(t *testing.T) {
	// First call fails, second succeeds: should NOT use fallback.
	withRandReader(t, &retryingReader{failsLeft: 1})

	id := MustGenerateTraceID()
	if strings.HasPrefix(id, FallbackTraceIDPrefix) {
		t.Fatalf("expected real id after retry, got fallback %q", id)
	}
	if len(id) != 32 {
		t.Errorf("expected 32-hex id after successful retry, got len=%d (%q)", len(id), id)
	}
}

func TestMustGenerateSpanID_RetriesOnceBeforeFallback(t *testing.T) {
	withRandReader(t, &retryingReader{failsLeft: 1})

	id := MustGenerateSpanID()
	if strings.HasPrefix(id, FallbackSpanIDPrefix) {
		t.Fatalf("expected real id after retry, got fallback %q", id)
	}
	if len(id) != 16 {
		t.Errorf("expected 16-hex id after successful retry, got len=%d (%q)", len(id), id)
	}
}

// TestGenerateIDs_NoCollisionUnderConcurrency exercises the normal
// (working-rand) path from many goroutines and asserts no two IDs ever
// coincide. The historical bug collapsed every concurrent caller onto
// the same all-zero string; this test would have caught it.
func TestGenerateIDs_NoCollisionUnderConcurrency(t *testing.T) {
	const workers = 64
	const perWorker = 200

	var traceSet sync.Map
	var spanSet sync.Map
	var collisions int64
	var collisionMu sync.Mutex

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				tid, err := GenerateTraceID()
				if err != nil {
					t.Errorf("GenerateTraceID: %v", err)
					return
				}
				if _, loaded := traceSet.LoadOrStore(tid, struct{}{}); loaded {
					collisionMu.Lock()
					collisions++
					collisionMu.Unlock()
				}

				sid, err := GenerateSpanID()
				if err != nil {
					t.Errorf("GenerateSpanID: %v", err)
					return
				}
				if _, loaded := spanSet.LoadOrStore(sid, struct{}{}); loaded {
					collisionMu.Lock()
					collisions++
					collisionMu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	if collisions != 0 {
		t.Fatalf("%d collisions across %d concurrent IDs; rand path is broken", collisions, workers*perWorker*2)
	}
}

// TestStartTrace_RandFailureProducesDistinguishableMarker locks down
// the composite helper used by router middleware: when entropy is dead
// the resulting context must carry per-call distinguishable IDs (not
// all-zero hex, and not a single shared constant).
func TestStartTrace_RandFailureProducesDistinguishableMarker(t *testing.T) {
	withRandReader(t, failingReader{})

	_, traceID1, spanID1 := StartTrace(context.Background())
	_, traceID2, spanID2 := StartTrace(context.Background())

	if !fallbackTraceIDRe.MatchString(traceID1) {
		t.Errorf("StartTrace trace id %q does not match fallback shape", traceID1)
	}
	if !fallbackSpanIDRe.MatchString(spanID1) {
		t.Errorf("StartTrace span id %q does not match fallback shape", spanID1)
	}
	if traceID1 == traceID2 {
		t.Fatalf("StartTrace produced identical trace ids %q across two calls; correlation broken", traceID1)
	}
	if spanID1 == spanID2 {
		t.Fatalf("StartTrace produced identical span ids %q across two calls; correlation broken", spanID1)
	}
	if traceID1 == strings.Repeat("0", 32) {
		t.Fatal("regression: StartTrace returned all-zero trace id")
	}
	if spanID1 == strings.Repeat("0", 16) {
		t.Fatal("regression: StartTrace returned all-zero span id")
	}
}

func isHexOnly(s string) bool {
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
