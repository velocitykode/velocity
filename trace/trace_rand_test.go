package trace

import (
	"context"
	"errors"
	"io"
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

func TestMustGenerateTraceID_FallsBackToDistinguishableMarker(t *testing.T) {
	withRandReader(t, failingReader{})

	id := MustGenerateTraceID()
	if id != FallbackTraceID {
		t.Fatalf("expected fallback marker %q, got %q", FallbackTraceID, id)
	}
	// Fallback marker must NOT be a 32-hex string so APM cannot
	// correlate it with a real trace.
	if len(id) == 32 && isHexOnly(id) {
		t.Errorf("fallback marker must not be 32-hex; got %q", id)
	}
	if id == strings.Repeat("0", 32) {
		t.Errorf("fallback marker must not be all-zero hex; got %q", id)
	}
}

func TestMustGenerateSpanID_FallsBackToDistinguishableMarker(t *testing.T) {
	withRandReader(t, failingReader{})

	id := MustGenerateSpanID()
	if id != FallbackSpanID {
		t.Fatalf("expected fallback marker %q, got %q", FallbackSpanID, id)
	}
	if len(id) == 16 && isHexOnly(id) {
		t.Errorf("fallback marker must not be 16-hex; got %q", id)
	}
	if id == strings.Repeat("0", 16) {
		t.Errorf("fallback marker must not be all-zero hex; got %q", id)
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
	if id == FallbackTraceID {
		t.Fatalf("expected real id after retry, got fallback %q", id)
	}
	if len(id) != 32 {
		t.Errorf("expected 32-hex id after successful retry, got len=%d (%q)", len(id), id)
	}
}

func TestMustGenerateSpanID_RetriesOnceBeforeFallback(t *testing.T) {
	withRandReader(t, &retryingReader{failsLeft: 1})

	id := MustGenerateSpanID()
	if id == FallbackSpanID {
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
// the resulting context must carry the fallback markers, not all-zero
// hex.
func TestStartTrace_RandFailureProducesDistinguishableMarker(t *testing.T) {
	withRandReader(t, failingReader{})

	_, traceID, spanID := StartTrace(context.Background())
	if traceID != FallbackTraceID {
		t.Errorf("StartTrace trace id: got %q want fallback %q", traceID, FallbackTraceID)
	}
	if spanID != FallbackSpanID {
		t.Errorf("StartTrace span id: got %q want fallback %q", spanID, FallbackSpanID)
	}
	if traceID == strings.Repeat("0", 32) {
		t.Fatal("regression: StartTrace returned all-zero trace id")
	}
	if spanID == strings.Repeat("0", 16) {
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
