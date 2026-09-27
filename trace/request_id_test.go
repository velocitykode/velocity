package trace

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// TestGenerateRequestID_Format pins the documented format: 20 lowercase hex
// characters, the first 8 the Unix time in seconds.
func TestGenerateRequestID_Format(t *testing.T) {
	id := GenerateRequestID()
	if len(id) != 20 || !isLowerHex(id) {
		t.Fatalf("GenerateRequestID() = %q, want 20 lowercase hex characters", id)
	}
	if !ValidRequestID(id) {
		t.Fatalf("GenerateRequestID() = %q is not a ValidRequestID", id)
	}
	if other := GenerateRequestID(); other == id {
		t.Errorf("two ids collided: %q", id)
	}
}

func TestValidRequestID(t *testing.T) {
	for _, id := range []string{
		"a",
		"66f6a0b2002a9c41e07b",
		"3f2b8c1e-9a4d-4c3b-8e2f-1a2b3c4d5e6f",
		"dGVzdA==",
		"a+b/c=d",
		"edge:worker@host_1.2",
		strings.Repeat("x", 128),
	} {
		if !ValidRequestID(id) {
			t.Errorf("ValidRequestID(%q) = false, want true", id)
		}
	}
	for _, id := range []string{
		"",
		strings.Repeat("x", 129),
		"has space",
		"tab\there",
		"line\nbreak",
		"carriage\rreturn",
		`quote"d`,
		"semi;colon",
		"comma,list",
		"non-ascii-é",
		"nul\x00byte",
	} {
		if ValidRequestID(id) {
			t.Errorf("ValidRequestID(%q) = true, want false", id)
		}
	}
}

func TestWithRequestID(t *testing.T) {
	if got := GetRequestID(context.Background()); got != "" {
		t.Errorf("empty ctx request id = %q", got)
	}
	//lint:ignore SA1012 exercising the nil-context code path is the point of this test
	if got := GetRequestID(nil); got != "" {
		t.Errorf("nil ctx request id = %q", got)
	}
	ctx := WithRequestID(context.Background(), "req-1")
	if got := GetRequestID(ctx); got != "req-1" {
		t.Errorf("GetRequestID = %q, want req-1", got)
	}
	if got := GetRequestID(WithRequestID(ctx, "req-2")); got != "req-2" {
		t.Errorf("inner WithRequestID = %q, want req-2", got)
	}
}

func TestWithLazyRequestID(t *testing.T) {
	ctx, lazy := WithLazyRequestID(context.Background())
	first := GetRequestID(ctx)
	if len(first) != 20 {
		t.Fatalf("lazy request id = %q, want a generated id", first)
	}
	if lazy.ID() != first || GetRequestID(ctx) != first {
		t.Errorf("lazy id changed across reads: %q then %q/%q", first, lazy.ID(), GetRequestID(ctx))
	}
	if raw, ok := ctx.Value(requestIDKey).(string); !ok || raw != first {
		t.Errorf("raw key value = %#v, want the string %q", ctx.Value(requestIDKey), first)
	}
	// Other keys pass through to the wrapped context.
	traced, _ := WithLazyRequestID(WithTrace(context.Background(), w3cTrace, w3cSpan))
	if GetTraceID(traced) != w3cTrace {
		t.Errorf("wrapped trace id = %q", GetTraceID(traced))
	}
}

func TestWithLazyRequestID_ConcurrentReads(t *testing.T) {
	ctx, _ := WithLazyRequestID(context.Background())
	const n = 32
	ids := make([]string, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			ids[i] = GetRequestID(ctx)
		}(i)
	}
	wg.Wait()
	for i := 1; i < n; i++ {
		if ids[i] != ids[0] {
			t.Fatalf("concurrent reads disagreed: %q vs %q", ids[0], ids[i])
		}
	}
}
