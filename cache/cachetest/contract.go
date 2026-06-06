// Package cachetest provides executable specifications (contract tests) for
// [cache.Store] and [drivers.Locker] implementations.
//
// Every framework-shipped cache driver runs through these runners in CI;
// third-party drivers are expected to do the same so contract drift surfaces
// before deployment.
package cachetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/cache/drivers"
)

// StoreFactory returns a fresh empty Store per sub-test. The factory
// owns cleanup (typically via t.Cleanup).
//
// Pass a bare StoreFactory to RunStoreContractTests for stores whose TTL
// expiry follows wall-clock time (memory, file). Stores backed by a
// clock-skewed test fixture (miniredis) must use StoreFactoryWithClock so
// the TTL-expiry invariant can advance the fixture's clock instead of
// relying on time.Sleep.
type StoreFactory func(t *testing.T) cache.Store

// StoreFactoryWithClock is the clock-aware variant of StoreFactory. The
// Advance callback is invoked by the TTL-expiry t.Run to push the
// fixture's clock past the entry's TTL. For wall-clock-based stores,
// pass nil for Advance and the runner falls back to time.Sleep.
type StoreFactoryWithClock struct {
	New     func(t *testing.T) cache.Store
	Advance func(d time.Duration)
}

// LockerFactory returns a fresh empty Locker per sub-test.
type LockerFactory func(t *testing.T) drivers.Locker

// RunStoreContractTests is the executable specification of [cache.Store].
// Delegates to RunStoreContractTestsWithClock with a wall-clock Advance.
func RunStoreContractTests(t *testing.T, factory StoreFactory) {
	t.Helper()
	RunStoreContractTestsWithClock(t, StoreFactoryWithClock{
		New:     factory,
		Advance: func(d time.Duration) { time.Sleep(d) },
	})
}

// RunStoreContractTestsWithClock is the clock-aware variant of
// RunStoreContractTests. The Advance callback is used to expire TTLs
// against test fixtures whose clock does not follow time.Now (miniredis).
func RunStoreContractTestsWithClock(t *testing.T, f StoreFactoryWithClock) {
	t.Helper()
	if f.Advance == nil {
		f.Advance = func(d time.Duration) { time.Sleep(d) }
	}
	factory := f.New
	advance := f.Advance

	t.Run("GetCtx_Missing_ReturnsZeroFalse", func(t *testing.T) {
		s := factory(t)
		v, ok := s.GetCtx(context.Background(), "absent")
		if ok {
			t.Fatalf("expected miss on absent key, got hit %v", v)
		}
		if v != nil {
			t.Fatalf("expected nil value on miss, got %v", v)
		}
	})

	t.Run("PutCtx_Then_GetCtx_RoundTripsValue", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		if err := s.PutCtx(ctx, "k1", "v1", time.Minute); err != nil {
			t.Fatalf("PutCtx: %v", err)
		}
		v, ok := s.GetCtx(ctx, "k1")
		if !ok {
			t.Fatal("expected hit after Put")
		}
		if v != "v1" {
			t.Fatalf("expected v1, got %v", v)
		}
	})

	t.Run("PutCtx_Then_GetStringCtx_RoundTripsString", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		// A string stored via PutCtx must come back byte-identical through
		// GetStringCtx. Serializing drivers (file, redis) JSON-encode on Put;
		// GetStringCtx must undo that, not return the quoted/escaped form.
		const want = `hello "world"` + "\n\t\\ end"
		if err := s.PutCtx(ctx, "gs-str", want, time.Minute); err != nil {
			t.Fatalf("PutCtx: %v", err)
		}
		got, ok := s.GetStringCtx(ctx, "gs-str")
		if !ok {
			t.Fatal("expected hit after Put")
		}
		if got != want {
			t.Fatalf("GetStringCtx round-trip: got %q, want %q", got, want)
		}
	})

	t.Run("PutCtx_Then_GetStringCtx_RoundTripsBinaryString", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		// A string carrying invalid UTF-8 (binary bytes) must survive
		// byte-identically. Serializing drivers must not coerce invalid
		// UTF-8 to the U+FFFD replacement rune via plain JSON encoding.
		want := string([]byte{0xff, 0x00, 'x', 0xfe, 0x80})
		if err := s.PutCtx(ctx, "gs-bin", want, time.Minute); err != nil {
			t.Fatalf("PutCtx: %v", err)
		}
		got, ok := s.GetStringCtx(ctx, "gs-bin")
		if !ok {
			t.Fatal("expected hit after Put")
		}
		if got != want {
			t.Fatalf("binary string round-trip: got %q, want %q", got, want)
		}
	})

	t.Run("PutCtx_Then_GetCtx_RoundTripsMapValueVerbatim", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		// The serialization layer reserves no value shape: a plain map must
		// round-trip as a map, never be coerced into a string. This guards
		// against an internal string-envelope collision -- the map below
		// deliberately resembles a former envelope shape.
		in := map[string]interface{}{"__velocity_cache_b64str__": "eA=="}
		if err := s.PutCtx(ctx, "mapval", in, time.Minute); err != nil {
			t.Fatalf("PutCtx: %v", err)
		}
		v, ok := s.GetCtx(ctx, "mapval")
		if !ok {
			t.Fatal("expected hit after Put")
		}
		m, ok := v.(map[string]interface{})
		if !ok {
			t.Fatalf("expected map value, got %T (%v)", v, v)
		}
		if m["__velocity_cache_b64str__"] != "eA==" {
			t.Fatalf("map value corrupted on round-trip: %v", m)
		}
	})

	t.Run("GetStringCtx_NonStringValue_ReadsAsMiss", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		// GetStringCtx is the typed string accessor: a slot holding a
		// non-string value must report (\"\", false), not a coerced form.
		if err := s.PutCtx(ctx, "gs-obj", map[string]any{"a": 1}, time.Minute); err != nil {
			t.Fatalf("PutCtx: %v", err)
		}
		if v, ok := s.GetStringCtx(ctx, "gs-obj"); ok {
			t.Fatalf("expected non-string slot to miss GetStringCtx, got %q", v)
		}
	})

	t.Run("GetStringCtx_AbsentKey_ReadsAsMiss", func(t *testing.T) {
		s := factory(t)
		if v, ok := s.GetStringCtx(context.Background(), "gs-absent"); ok {
			t.Fatalf("expected miss for absent key, got %q", v)
		}
	})

	t.Run("PutCtx_ExpiredTTL_ReadsAsMiss", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		// Short TTL + Advance: the fixture clock moves past the entry's
		// expiry so the next Get must miss. Wall-clock-based stores
		// rely on Advance == time.Sleep.
		if err := s.PutCtx(ctx, "ttl-expired", "v", 10*time.Millisecond); err != nil {
			t.Fatalf("PutCtx: %v", err)
		}
		advance(50 * time.Millisecond)
		if _, ok := s.GetCtx(ctx, "ttl-expired"); ok {
			t.Fatal("expected miss after TTL expiry")
		}
	})

	t.Run("ForgetCtx_RemovesValue", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		if err := s.PutCtx(ctx, "forget-me", "v", time.Minute); err != nil {
			t.Fatalf("PutCtx: %v", err)
		}
		if err := s.ForgetCtx(ctx, "forget-me"); err != nil {
			t.Fatalf("ForgetCtx: %v", err)
		}
		if _, ok := s.GetCtx(ctx, "forget-me"); ok {
			t.Fatal("expected miss after Forget")
		}
	})

	t.Run("ForgetCtx_AbsentKey_IsNoop", func(t *testing.T) {
		s := factory(t)
		// Forget on a key that never existed must not error.
		if err := s.ForgetCtx(context.Background(), "never-existed"); err != nil {
			t.Fatalf("ForgetCtx on absent key: %v", err)
		}
	})

	t.Run("AddCtx_NewKey_ReturnsTrue", func(t *testing.T) {
		s := factory(t)
		inserted, err := s.AddCtx(context.Background(), "add-1", "v", time.Minute)
		if err != nil {
			t.Fatalf("AddCtx: %v", err)
		}
		if !inserted {
			t.Fatal("expected AddCtx to return true for new key")
		}
	})

	t.Run("AddCtx_ExistingKey_ReturnsFalseNoOverwrite", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_, _ = s.AddCtx(ctx, "add-2", "first", time.Minute)
		inserted, err := s.AddCtx(ctx, "add-2", "second", time.Minute)
		if err != nil {
			t.Fatalf("AddCtx: %v", err)
		}
		if inserted {
			t.Fatal("expected AddCtx to return false on contention")
		}
		v, _ := s.GetCtx(ctx, "add-2")
		if v != "first" {
			t.Fatalf("AddCtx overwrote existing value: got %v, want \"first\"", v)
		}
	})

	t.Run("AddCtx_Concurrent_ExactlyOneWinner", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		const racers = 20
		var wins atomic.Int32
		var wg sync.WaitGroup
		for i := 0; i < racers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				ok, err := s.AddCtx(ctx, "race-key", fmt.Sprintf("v-%d", i), time.Minute)
				if err == nil && ok {
					wins.Add(1)
				}
			}(i)
		}
		wg.Wait()
		if got := wins.Load(); got != 1 {
			t.Fatalf("AddCtx allowed %d winners; expected exactly 1", got)
		}
	})

	t.Run("ForeverCtx_PersistsBeyondShortTTL", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		if err := s.ForeverCtx(ctx, "forever", "v"); err != nil {
			t.Fatalf("ForeverCtx: %v", err)
		}
		// A TTLless value must still be present after the clock advances.
		advance(50 * time.Millisecond)
		if _, ok := s.GetCtx(ctx, "forever"); !ok {
			t.Fatal("expected Forever entry to outlive a clock advance")
		}
	})

	t.Run("IncrementCtx_NewKey_StartsFromZero", func(t *testing.T) {
		s := factory(t)
		v, err := s.IncrementCtx(context.Background(), "inc-new", 5)
		if err != nil {
			t.Fatalf("IncrementCtx: %v", err)
		}
		if v != 5 {
			t.Fatalf("expected 5, got %d", v)
		}
	})

	t.Run("IncrementCtx_Existing_AccumulatesAtomically", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_, _ = s.IncrementCtx(ctx, "inc-acc", 1)
		_, _ = s.IncrementCtx(ctx, "inc-acc", 2)
		v, _ := s.IncrementCtx(ctx, "inc-acc", 3)
		if v != 6 {
			t.Fatalf("expected 6, got %d", v)
		}
	})

	t.Run("IncrementCtx_NonNumericValue_Errors", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		// Incrementing a key holding a non-numeric value must error, never
		// silently reset the counter to the increment amount. Covers a plain
		// string and a binary (0x00-framed on serializing drivers) string.
		if err := s.PutCtx(ctx, "inc-str", "hello", time.Minute); err != nil {
			t.Fatalf("PutCtx: %v", err)
		}
		if _, err := s.IncrementCtx(ctx, "inc-str", 1); err == nil {
			t.Fatal("expected error incrementing a string value")
		}
		if v, ok := s.GetStringCtx(ctx, "inc-str"); !ok || v != "hello" {
			t.Fatalf("string value must survive a failed Increment: got %q ok=%v", v, ok)
		}

		if err := s.PutCtx(ctx, "inc-bin", string([]byte{0xff, 0x00, 'x'}), time.Minute); err != nil {
			t.Fatalf("PutCtx: %v", err)
		}
		if _, err := s.IncrementCtx(ctx, "inc-bin", 1); err == nil {
			t.Fatal("expected error incrementing a binary string value")
		}
	})

	t.Run("DecrementCtx_Existing_Subtracts", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_, _ = s.IncrementCtx(ctx, "dec-key", 10)
		v, err := s.DecrementCtx(ctx, "dec-key", 4)
		if err != nil {
			t.Fatalf("DecrementCtx: %v", err)
		}
		if v != 6 {
			t.Fatalf("expected 6, got %d", v)
		}
	})

	t.Run("HasCtx_PresentAndAbsent", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.PutCtx(ctx, "has-it", "v", time.Minute)
		if !s.HasCtx(ctx, "has-it") {
			t.Fatal("expected HasCtx=true for present key")
		}
		if s.HasCtx(ctx, "absent-it") {
			t.Fatal("expected HasCtx=false for absent key")
		}
	})

	t.Run("FlushCtx_RemovesAllValues", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.PutCtx(ctx, "k1", "v1", time.Minute)
		_ = s.PutCtx(ctx, "k2", "v2", time.Minute)
		if err := s.FlushCtx(ctx); err != nil {
			t.Fatalf("FlushCtx: %v", err)
		}
		if _, ok := s.GetCtx(ctx, "k1"); ok {
			t.Fatal("k1 survived Flush")
		}
		if _, ok := s.GetCtx(ctx, "k2"); ok {
			t.Fatal("k2 survived Flush")
		}
	})

	t.Run("ManyCtx_ReturnsHitsOnly", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.PutCtx(ctx, "many-a", "A", time.Minute)
		_ = s.PutCtx(ctx, "many-b", "B", time.Minute)
		got := s.ManyCtx(ctx, []string{"many-a", "many-b", "many-missing"})
		// Drivers may return only hits OR return a map with all keys
		// (missing as nil). Verify hits are correct.
		if got["many-a"] != "A" {
			t.Fatalf("expected many-a=A, got %v", got["many-a"])
		}
		if got["many-b"] != "B" {
			t.Fatalf("expected many-b=B, got %v", got["many-b"])
		}
		if v, ok := got["many-missing"]; ok && v != nil {
			t.Fatalf("expected miss for many-missing, got %v", v)
		}
	})

	t.Run("PutManyCtx_BulkInsert", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		if err := s.PutManyCtx(ctx, map[string]interface{}{
			"bulk-1": "one",
			"bulk-2": "two",
		}, time.Minute); err != nil {
			t.Fatalf("PutManyCtx: %v", err)
		}
		if v, _ := s.GetCtx(ctx, "bulk-1"); v != "one" {
			t.Fatalf("bulk-1: got %v", v)
		}
		if v, _ := s.GetCtx(ctx, "bulk-2"); v != "two" {
			t.Fatalf("bulk-2: got %v", v)
		}
	})

	t.Run("GetPrefix_Returns_Configured_Prefix", func(t *testing.T) {
		s := factory(t)
		// Contract: GetPrefix returns a stable string. We do not enforce
		// any particular value; the prefix is part of the constructor.
		_ = s.GetPrefix() // must not panic
	})
}

// RunLockerContractTests is the executable specification of
// [drivers.Locker]. Each backend (memory, redis, file) must pass this runner.
func RunLockerContractTests(t *testing.T, factory LockerFactory) {
	t.Helper()

	t.Run("Lock_NewKey_Acquires", func(t *testing.T) {
		l := factory(t)
		lk := l.Lock("contract-acquire", time.Second)
		if !lk.Get(context.Background()) {
			t.Fatal("expected Get to acquire a fresh lock")
		}
		_ = lk.Release(context.Background())
	})

	t.Run("Lock_Held_GetReturnsFalse", func(t *testing.T) {
		l := factory(t)
		ctx := context.Background()
		lk1 := l.Lock("contract-contention", time.Second)
		if !lk1.Get(ctx) {
			t.Fatal("first acquire failed")
		}
		lk2 := l.Lock("contract-contention", time.Second)
		if lk2.Get(ctx) {
			t.Fatal("expected second Get to fail while held")
		}
		_ = lk1.Release(ctx)
	})

	t.Run("Lock_Release_AllowsReacquire", func(t *testing.T) {
		l := factory(t)
		ctx := context.Background()
		lk := l.Lock("contract-reacquire", time.Second)
		if !lk.Get(ctx) {
			t.Fatal("first acquire failed")
		}
		_ = lk.Release(ctx)
		lk2 := l.Lock("contract-reacquire", time.Second)
		if !lk2.Get(ctx) {
			t.Fatal("expected reacquire to succeed after Release")
		}
		_ = lk2.Release(ctx)
	})

	t.Run("Lock_ZeroTTL_RejectsAtAcquire", func(t *testing.T) {
		l := factory(t)
		lk := l.Lock("zero-ttl", 0)
		_, err := lk.GetWithErr(context.Background())
		if err == nil {
			t.Fatal("expected error acquiring zero-TTL lock")
		}
		if !errors.Is(err, drivers.ErrInvalidLockTTL) {
			t.Fatalf("expected ErrInvalidLockTTL, got %v", err)
		}
	})

	t.Run("Lock_NegativeTTL_RejectsAtAcquire", func(t *testing.T) {
		l := factory(t)
		lk := l.Lock("neg-ttl", -time.Second)
		_, err := lk.GetWithErr(context.Background())
		if err == nil {
			t.Fatal("expected error acquiring negative-TTL lock")
		}
		if !errors.Is(err, drivers.ErrInvalidLockTTL) {
			t.Fatalf("expected ErrInvalidLockTTL, got %v", err)
		}
	})

	t.Run("Lock_Owner_StableAcrossCalls", func(t *testing.T) {
		l := factory(t)
		lk := l.Lock("owner-stable", time.Second)
		o1 := lk.Owner()
		o2 := lk.Owner()
		if o1 != o2 {
			t.Fatalf("Owner not stable: %q vs %q", o1, o2)
		}
		if o1 == "" {
			t.Fatal("expected non-empty owner")
		}
	})

	t.Run("Lock_Run_AcquiresExecutesReleases", func(t *testing.T) {
		l := factory(t)
		lk := l.Lock("run-acquire", time.Second)
		ran := false
		if err := lk.Run(context.Background(), func() { ran = true }); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if !ran {
			t.Fatal("Run did not invoke callback")
		}
		// Subsequent acquire must succeed: Run releases.
		lk2 := l.Lock("run-acquire", time.Second)
		if !lk2.Get(context.Background()) {
			t.Fatal("expected Run to release the lock")
		}
		_ = lk2.Release(context.Background())
	})

	t.Run("Lock_Run_PanicReleases", func(t *testing.T) {
		l := factory(t)
		lk := l.Lock("run-panic", time.Second)
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("expected callback panic to propagate")
				}
			}()
			_ = lk.Run(context.Background(), func() { panic("boom") })
		}()
		// Lock must still be free.
		lk2 := l.Lock("run-panic", time.Second)
		if !lk2.Get(context.Background()) {
			t.Fatal("expected Run to release on panic")
		}
		_ = lk2.Release(context.Background())
	})

	t.Run("RestoreLock_BindsToExistingOwner", func(t *testing.T) {
		l := factory(t)
		lk := l.Lock("restore-key", time.Second)
		if !lk.Get(context.Background()) {
			t.Fatal("acquire failed")
		}
		restored := l.RestoreLock("restore-key", lk.Owner())
		if restored.Owner() != lk.Owner() {
			t.Fatalf("restored owner mismatch: %q vs %q", restored.Owner(), lk.Owner())
		}
		// Restored lock can release the original.
		if !restored.Release(context.Background()) {
			t.Fatal("restored lock failed to Release")
		}
	})
}
