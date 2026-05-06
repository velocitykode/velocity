package flags

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

// stubProvider is a minimal Provider used by ctx/default precedence tests.
// Its responses are static per name so tests can assert which provider
// answered.
type stubProvider struct {
	name string
	on   map[string]bool
	hits atomic.Int32
}

func (s *stubProvider) Enabled(_ context.Context, name string) bool {
	s.hits.Add(1)
	return s.on[name]
}

// resetDefault clears the package-level default so tests do not leak state.
func resetDefault(t *testing.T) {
	t.Helper()
	prev := Default()
	SetDefault(nil)
	t.Cleanup(func() { SetDefault(prev) })
}

func TestEnabled_FromCtx(t *testing.T) {
	resetDefault(t)

	ctxProv := &stubProvider{name: "ctx", on: map[string]bool{"feature": true}}
	defProv := &stubProvider{name: "def", on: map[string]bool{"feature": false}}
	SetDefault(defProv)

	ctx := WithProvider(context.Background(), ctxProv)

	if !Enabled(ctx, "feature") {
		t.Fatalf("expected ctx provider to win and return true")
	}
	if ctxProv.hits.Load() != 1 {
		t.Fatalf("ctx provider should have been called exactly once, got %d", ctxProv.hits.Load())
	}
	if defProv.hits.Load() != 0 {
		t.Fatalf("default provider must not be consulted when ctx provides one")
	}
}

func TestEnabled_FromDefault(t *testing.T) {
	resetDefault(t)

	defProv := &stubProvider{name: "def", on: map[string]bool{"feature": true}}
	SetDefault(defProv)

	if !Enabled(context.Background(), "feature") {
		t.Fatalf("expected default provider to answer true")
	}
	if defProv.hits.Load() != 1 {
		t.Fatalf("default should have been called once, got %d", defProv.hits.Load())
	}
}

func TestEnabled_NoProvider_FalseSafe(t *testing.T) {
	resetDefault(t)

	if Enabled(context.Background(), "anything") {
		t.Fatalf("expected false when no provider is configured")
	}
	// nil context must also be handled gracefully.
	//nolint:staticcheck // SA1012: deliberate nil-ctx safety check.
	if Enabled(nil, "anything") {
		t.Fatalf("expected false on nil context with no default")
	}
}

func TestEnabled_MissingFlag_DefaultsFalse(t *testing.T) {
	resetDefault(t)

	prov := &stubProvider{on: map[string]bool{"known": true}}
	SetDefault(prov)

	if Enabled(context.Background(), "unknown") {
		t.Fatalf("unknown flag should default to false")
	}
}

func TestWithProvider_NilContext(t *testing.T) {
	// WithProvider must not panic on a nil context; it should normalize to
	// context.Background() and still attach the provider.
	prov := &stubProvider{on: map[string]bool{"x": true}}
	//nolint:staticcheck // SA1012: deliberate nil-ctx safety check.
	ctx := WithProvider(nil, prov)
	if ctx == nil {
		t.Fatalf("expected non-nil context")
	}
	if !Enabled(ctx, "x") {
		t.Fatalf("expected provider attached via nil-ctx path to answer")
	}
}

func TestSetDefault_NilClears(t *testing.T) {
	resetDefault(t)

	SetDefault(&stubProvider{on: map[string]bool{"a": true}})
	if !Enabled(context.Background(), "a") {
		t.Fatalf("expected default to answer before clear")
	}
	SetDefault(nil)
	if Default() != nil {
		t.Fatalf("expected Default() == nil after SetDefault(nil)")
	}
	if Enabled(context.Background(), "a") {
		t.Fatalf("expected false after clearing default")
	}
}

func TestMemoryProvider_SetGet(t *testing.T) {
	m := NewMemoryProvider(map[string]bool{"seeded": true})

	if !m.Enabled(context.Background(), "seeded") {
		t.Fatalf("expected seeded flag to be on")
	}
	if m.Enabled(context.Background(), "missing") {
		t.Fatalf("missing flag must default to false")
	}

	m.Set("seeded", false)
	if m.Enabled(context.Background(), "seeded") {
		t.Fatalf("Set(false) should disable the flag")
	}

	m.Set("new", true)
	if !m.Enabled(context.Background(), "new") {
		t.Fatalf("Set(true) should enable the flag")
	}
}

func TestNewMemoryProvider_NilInitial(t *testing.T) {
	m := NewMemoryProvider(nil)
	if m == nil {
		t.Fatalf("constructor must not return nil")
	}
	if m.Enabled(context.Background(), "anything") {
		t.Fatalf("nil-seeded provider should answer false for any flag")
	}
}

func TestNewMemoryProvider_CopiesInitial(t *testing.T) {
	src := map[string]bool{"a": true}
	m := NewMemoryProvider(src)

	// Mutate the caller's map after construction; provider must not change.
	src["a"] = false
	src["b"] = true

	if !m.Enabled(context.Background(), "a") {
		t.Fatalf("provider must snapshot initial map; outside mutation leaked in")
	}
	if m.Enabled(context.Background(), "b") {
		t.Fatalf("provider must snapshot initial map; outside addition leaked in")
	}
}

func TestMemoryProvider_SetAll_ReplacesAll(t *testing.T) {
	m := NewMemoryProvider(map[string]bool{"old": true, "stale": true})

	m.SetAll(map[string]bool{"fresh": true})

	if m.Enabled(context.Background(), "old") {
		t.Fatalf("SetAll must drop pre-existing flags")
	}
	if m.Enabled(context.Background(), "stale") {
		t.Fatalf("SetAll must drop pre-existing flags")
	}
	if !m.Enabled(context.Background(), "fresh") {
		t.Fatalf("SetAll must install the new flags")
	}

	// SetAll(nil) clears everything.
	m.SetAll(nil)
	if m.Enabled(context.Background(), "fresh") {
		t.Fatalf("SetAll(nil) must clear flags")
	}
}

func TestMemoryProvider_SetAll_CopiesInput(t *testing.T) {
	m := NewMemoryProvider(nil)
	src := map[string]bool{"a": true}
	m.SetAll(src)

	src["a"] = false
	src["b"] = true

	if !m.Enabled(context.Background(), "a") {
		t.Fatalf("SetAll must snapshot input; outside mutation leaked in")
	}
	if m.Enabled(context.Background(), "b") {
		t.Fatalf("SetAll must snapshot input; outside addition leaked in")
	}
}

func TestMemoryProvider_Concurrent(t *testing.T) {
	const (
		writers       = 8
		readers       = 16
		opsPerWorker  = 1000
		flagPoolCount = 32
	)

	m := NewMemoryProvider(nil)

	var wg sync.WaitGroup
	wg.Add(writers + readers + 1)

	// Writers: toggle individual flags.
	for i := 0; i < writers; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < opsPerWorker; j++ {
				name := flagName((id*opsPerWorker + j) % flagPoolCount)
				m.Set(name, j%2 == 0)
			}
		}(i)
	}

	// Readers: race against writers via Enabled.
	for i := 0; i < readers; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < opsPerWorker; j++ {
				name := flagName((id*opsPerWorker + j) % flagPoolCount)
				_ = m.Enabled(context.Background(), name)
			}
		}(i)
	}

	// Bulk replacer: exercises the SetAll write path concurrently.
	go func() {
		defer wg.Done()
		for j := 0; j < opsPerWorker; j++ {
			next := make(map[string]bool, flagPoolCount)
			for k := 0; k < flagPoolCount; k++ {
				next[flagName(k)] = (k+j)%3 == 0
			}
			m.SetAll(next)
		}
	}()

	wg.Wait()
}

func TestSetDefault_Concurrent(t *testing.T) {
	resetDefault(t)

	provA := &stubProvider{on: map[string]bool{"x": true}}
	provB := &stubProvider{on: map[string]bool{"x": false}}

	const (
		swappers     = 4
		readers      = 16
		opsPerWorker = 1000
	)

	var wg sync.WaitGroup
	wg.Add(swappers + readers)

	for i := 0; i < swappers; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < opsPerWorker; j++ {
				if (id+j)%2 == 0 {
					SetDefault(provA)
				} else {
					SetDefault(provB)
				}
			}
		}(i)
	}

	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < opsPerWorker; j++ {
				_ = Enabled(context.Background(), "x")
			}
		}()
	}

	wg.Wait()
}

// flagName produces a deterministic flag name from an index without
// pulling in fmt for what is a hot loop in the concurrency test.
func flagName(i int) string {
	const alpha = "abcdefghijklmnopqrstuvwxyz"
	return string(alpha[i%len(alpha)]) + string(alpha[(i/len(alpha))%len(alpha)])
}
