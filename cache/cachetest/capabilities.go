package cachetest

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
)

// ReplacerStore is the intersection a factory must return for
// RunReplacerContractTests: a full Store that also implements the optional
// contract.CacheReplacer capability.
type ReplacerStore interface {
	contract.Cache
	contract.CacheReplacer
}

// ReplacerFactory returns a fresh empty replacer-capable store per sub-test.
type ReplacerFactory func(t *testing.T) ReplacerStore

// RunReplacerContractTests is the executable specification of
// [contract.CacheReplacer]. Advance pushes the fixture's clock; pass nil for
// wall-clock stores.
func RunReplacerContractTests(t *testing.T, factory ReplacerFactory, advance func(d time.Duration)) {
	t.Helper()
	if advance == nil {
		advance = func(d time.Duration) { time.Sleep(d) }
	}

	t.Run("ReplaceCtx_AbsentKey_ReturnsFalseAndNeverInserts", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		ok, err := s.ReplaceCtx(ctx, "rep-absent", "v", time.Minute)
		if err != nil {
			t.Fatalf("ReplaceCtx: %v", err)
		}
		if ok {
			t.Fatal("ReplaceCtx reported a write on an absent key")
		}
		if _, found := s.GetCtx(ctx, "rep-absent"); found {
			t.Fatal("ReplaceCtx inserted an absent key")
		}
	})

	t.Run("ReplaceCtx_PresentKey_WritesValueAndTTL", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		if err := s.PutCtx(ctx, "rep-1", "first", time.Minute); err != nil {
			t.Fatalf("PutCtx: %v", err)
		}
		ok, err := s.ReplaceCtx(ctx, "rep-1", "second", time.Minute)
		if err != nil {
			t.Fatalf("ReplaceCtx: %v", err)
		}
		if !ok {
			t.Fatal("ReplaceCtx returned false on a present key")
		}
		if v, _ := s.GetCtx(ctx, "rep-1"); v != "second" {
			t.Fatalf("value after ReplaceCtx = %v, want \"second\"", v)
		}
	})

	t.Run("ReplaceCtx_AfterForget_ReturnsFalse", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.PutCtx(ctx, "rep-del", "v", time.Minute)
		if err := s.ForgetCtx(ctx, "rep-del"); err != nil {
			t.Fatalf("ForgetCtx: %v", err)
		}
		ok, err := s.ReplaceCtx(ctx, "rep-del", "again", time.Minute)
		if err != nil {
			t.Fatalf("ReplaceCtx: %v", err)
		}
		if ok {
			t.Fatal("ReplaceCtx resurrected a forgotten key")
		}
		if _, found := s.GetCtx(ctx, "rep-del"); found {
			t.Fatal("forgotten key is readable after ReplaceCtx")
		}
	})

	t.Run("ReplaceCtx_ExpiredKey_ReturnsFalse", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.PutCtx(ctx, "rep-exp", "v", 30*time.Millisecond)
		advance(80 * time.Millisecond)
		ok, err := s.ReplaceCtx(ctx, "rep-exp", "again", time.Minute)
		if err != nil {
			t.Fatalf("ReplaceCtx: %v", err)
		}
		if ok {
			t.Fatal("ReplaceCtx revived an expired key")
		}
		if _, found := s.GetCtx(ctx, "rep-exp"); found {
			t.Fatal("expired key is readable after ReplaceCtx")
		}
	})

	t.Run("ReplaceCtx_ZeroTTL_KeepsForever", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.PutCtx(ctx, "rep-forever", "v", 30*time.Millisecond)
		ok, err := s.ReplaceCtx(ctx, "rep-forever", "kept", 0)
		if err != nil || !ok {
			t.Fatalf("ReplaceCtx: ok=%v err=%v", ok, err)
		}
		advance(80 * time.Millisecond)
		if v, found := s.GetCtx(ctx, "rep-forever"); !found || v != "kept" {
			t.Fatalf("ttl<=0 must keep the entry forever; got found=%v v=%v", found, v)
		}
	})
}

// SwapperStore is the intersection a factory must return for
// RunSwapperContractTests: a full Store that also implements the optional
// contract.CacheSwapper capability.
type SwapperStore interface {
	contract.Cache
	contract.CacheSwapper
}

// SwapperFactory returns a fresh empty swapper-capable store per sub-test.
type SwapperFactory func(t *testing.T) SwapperStore

// RunSwapperContractTests is the executable specification of
// [contract.CacheSwapper]. Advance pushes the fixture's clock; pass nil for
// wall-clock stores.
func RunSwapperContractTests(t *testing.T, factory SwapperFactory, advance func(d time.Duration)) {
	t.Helper()
	if advance == nil {
		advance = func(d time.Duration) { time.Sleep(d) }
	}

	t.Run("CompareAndSwapCtx_Match_WritesValue", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.PutCtx(ctx, "cas-1", "first", time.Minute)
		ok, err := s.CompareAndSwapCtx(ctx, "cas-1", "first", "second", time.Minute)
		if err != nil || !ok {
			t.Fatalf("CompareAndSwapCtx on a matching value: ok=%v err=%v", ok, err)
		}
		if v, _ := s.GetCtx(ctx, "cas-1"); v != "second" {
			t.Fatalf("value after swap = %v, want \"second\"", v)
		}
	})

	t.Run("CompareAndSwapCtx_Mismatch_WritesNothing", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.PutCtx(ctx, "cas-2", "first", time.Minute)
		// A write lands between the caller's read and its swap.
		_ = s.PutCtx(ctx, "cas-2", "intervening", time.Minute)
		ok, err := s.CompareAndSwapCtx(ctx, "cas-2", "first", "stale", time.Minute)
		if err != nil {
			t.Fatalf("CompareAndSwapCtx: %v", err)
		}
		if ok {
			t.Fatal("CompareAndSwapCtx swapped over a value it did not expect")
		}
		if v, _ := s.GetCtx(ctx, "cas-2"); v != "intervening" {
			t.Fatalf("value after a failed swap = %v, want the intervening write", v)
		}
	})

	t.Run("CompareAndSwapCtx_StructuredValue_ComparesStoredForm", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.PutCtx(ctx, "cas-map", map[string]any{"b": 2, "a": "x"}, time.Minute)
		ok, err := s.CompareAndSwapCtx(ctx, "cas-map", map[string]any{"a": "x", "b": 2}, "next", time.Minute)
		if err != nil || !ok {
			t.Fatalf("CompareAndSwapCtx on an equal map: ok=%v err=%v", ok, err)
		}
	})

	t.Run("CompareAndSwapCtx_AbsentOrForgotten_NeverInserts", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.PutCtx(ctx, "cas-del", "v", time.Minute)
		_ = s.ForgetCtx(ctx, "cas-del")
		for _, key := range []string{"cas-absent", "cas-del"} {
			ok, err := s.CompareAndSwapCtx(ctx, key, "v", "again", time.Minute)
			if err != nil {
				t.Fatalf("CompareAndSwapCtx(%s): %v", key, err)
			}
			if ok {
				t.Fatalf("CompareAndSwapCtx reported a write on %s", key)
			}
			if _, found := s.GetCtx(ctx, key); found {
				t.Fatalf("CompareAndSwapCtx inserted %s", key)
			}
		}
	})

	t.Run("CompareAndSwapCtx_ExpiredKey_ReturnsFalse", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.PutCtx(ctx, "cas-exp", "v", 30*time.Millisecond)
		advance(80 * time.Millisecond)
		ok, err := s.CompareAndSwapCtx(ctx, "cas-exp", "v", "again", time.Minute)
		if err != nil {
			t.Fatalf("CompareAndSwapCtx: %v", err)
		}
		if ok {
			t.Fatal("CompareAndSwapCtx revived an expired key")
		}
	})

	t.Run("CompareAndSwapCtx_TTL", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.PutCtx(ctx, "cas-short", "v", time.Minute)
		if ok, err := s.CompareAndSwapCtx(ctx, "cas-short", "v", "short", 30*time.Millisecond); err != nil || !ok {
			t.Fatalf("CompareAndSwapCtx: ok=%v err=%v", ok, err)
		}
		_ = s.PutCtx(ctx, "cas-forever", "v", 30*time.Millisecond)
		if ok, err := s.CompareAndSwapCtx(ctx, "cas-forever", "v", "kept", 0); err != nil || !ok {
			t.Fatalf("CompareAndSwapCtx: ok=%v err=%v", ok, err)
		}
		advance(80 * time.Millisecond)
		if _, found := s.GetCtx(ctx, "cas-short"); found {
			t.Fatal("a swapped value outlived its TTL")
		}
		if v, _ := s.GetCtx(ctx, "cas-forever"); v != "kept" {
			t.Fatalf("a swap with ttl 0 did not keep the value forever: %v", v)
		}
	})
}

// SetStore is the intersection a factory must return for
// RunSetStoreContractTests.
type SetStore interface {
	contract.Cache
	contract.CacheSetStore
}

// SetStoreFactory returns a fresh empty set-capable store per sub-test.
type SetStoreFactory func(t *testing.T) SetStore

// RunSetStoreContractTests is the executable specification of
// [contract.CacheSetStore]. Advance pushes the fixture's clock; pass nil for
// wall-clock stores.
func RunSetStoreContractTests(t *testing.T, factory SetStoreFactory, advance func(d time.Duration)) {
	t.Helper()
	if advance == nil {
		advance = func(d time.Duration) { time.Sleep(d) }
	}
	members := func(t *testing.T, s SetStore, key string) []string {
		t.Helper()
		got, err := s.SetMembersCtx(context.Background(), key)
		if err != nil {
			t.Fatalf("SetMembersCtx: %v", err)
		}
		sort.Strings(got)
		return got
	}
	equal := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}

	t.Run("SetMembersCtx_AbsentKey_ReturnsNil", func(t *testing.T) {
		s := factory(t)
		if got := members(t, s, "set-absent"); got != nil {
			t.Fatalf("absent key returned %v, want nil", got)
		}
	})

	t.Run("SetAddCtx_AddsAndDeduplicates", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		if err := s.SetAddCtx(ctx, "set-1", time.Minute, "a", "b"); err != nil {
			t.Fatalf("SetAddCtx: %v", err)
		}
		if err := s.SetAddCtx(ctx, "set-1", time.Minute, "b", "c"); err != nil {
			t.Fatalf("SetAddCtx: %v", err)
		}
		if got := members(t, s, "set-1"); !equal(got, []string{"a", "b", "c"}) {
			t.Fatalf("members = %v, want [a b c]", got)
		}
	})

	t.Run("SetRemoveCtx_RemovesAndDeletesWhenEmpty", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.SetAddCtx(ctx, "set-2", time.Minute, "a", "b")
		if err := s.SetRemoveCtx(ctx, "set-2", "a", "missing"); err != nil {
			t.Fatalf("SetRemoveCtx: %v", err)
		}
		if got := members(t, s, "set-2"); !equal(got, []string{"b"}) {
			t.Fatalf("members = %v, want [b]", got)
		}
		if err := s.SetRemoveCtx(ctx, "set-2", "b"); err != nil {
			t.Fatalf("SetRemoveCtx: %v", err)
		}
		if got := members(t, s, "set-2"); got != nil {
			t.Fatalf("members after removing last = %v, want nil", got)
		}
		if err := s.SetRemoveCtx(ctx, "set-2", "b"); err != nil {
			t.Fatalf("SetRemoveCtx on absent key must be nil, got %v", err)
		}
	})

	t.Run("SetAddCtx_TTL_ExpiresWholeSet", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.SetAddCtx(ctx, "set-ttl", 30*time.Millisecond, "a")
		advance(80 * time.Millisecond)
		if got := members(t, s, "set-ttl"); got != nil {
			t.Fatalf("expired set still lists %v", got)
		}
	})

	t.Run("SetAddCtx_ZeroTTL_KeepsForever", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.SetAddCtx(ctx, "set-forever", 30*time.Millisecond, "a")
		_ = s.SetAddCtx(ctx, "set-forever", 0, "b")
		advance(80 * time.Millisecond)
		if got := members(t, s, "set-forever"); !equal(got, []string{"a", "b"}) {
			t.Fatalf("ttl<=0 must keep the set forever; got %v", got)
		}
	})

	t.Run("SetAddCtx_TTL_NeverShortens", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.SetAddCtx(ctx, "set-long", time.Hour, "long")
		_ = s.SetAddCtx(ctx, "set-long", 30*time.Millisecond, "short")
		advance(80 * time.Millisecond)
		if got := members(t, s, "set-long"); !equal(got, []string{"long", "short"}) {
			t.Fatalf("a shorter later ttl shortened the set's life; got %v", got)
		}
	})

	t.Run("SetAddCtx_TTL_ExtendsWhenLonger", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.SetAddCtx(ctx, "set-extend", 30*time.Millisecond, "a")
		_ = s.SetAddCtx(ctx, "set-extend", time.Hour, "b")
		advance(80 * time.Millisecond)
		if got := members(t, s, "set-extend"); !equal(got, []string{"a", "b"}) {
			t.Fatalf("a longer later ttl did not extend the set's life; got %v", got)
		}
	})

	t.Run("SetAddCtx_ForeverThenTTL_StaysForever", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.SetAddCtx(ctx, "set-persist", 0, "a")
		_ = s.SetAddCtx(ctx, "set-persist", 30*time.Millisecond, "b")
		advance(80 * time.Millisecond)
		if got := members(t, s, "set-persist"); !equal(got, []string{"a", "b"}) {
			t.Fatalf("a positive ttl reinstated an expiry on a persisted set; got %v", got)
		}
	})

	t.Run("SetMembersCtx_ReturnedSliceIsDetached", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.SetAddCtx(ctx, "set-detach", time.Minute, "a")
		got, _ := s.SetMembersCtx(ctx, "set-detach")
		got[0] = "tampered"
		if again := members(t, s, "set-detach"); !equal(again, []string{"a"}) {
			t.Fatalf("mutating the returned slice changed the set: %v", again)
		}
	})

	t.Run("SetAddCtx_SetRemoveCtx_Concurrent_NoLostUpdates", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		const n = 50
		var wg sync.WaitGroup
		// Adders and removers interleave on one key; every member added by
		// an adder that is never removed must survive.
		for i := 0; i < n; i++ {
			wg.Add(2)
			go func(i int) {
				defer wg.Done()
				if err := s.SetAddCtx(ctx, "set-race", time.Minute, fmt.Sprintf("keep-%d", i), fmt.Sprintf("drop-%d", i)); err != nil {
					t.Errorf("SetAddCtx: %v", err)
				}
			}(i)
			go func(i int) {
				defer wg.Done()
				// Remove may run before the matching add; the final
				// sweep below removes whatever is left.
				if err := s.SetRemoveCtx(ctx, "set-race", fmt.Sprintf("drop-%d", i)); err != nil {
					t.Errorf("SetRemoveCtx: %v", err)
				}
			}(i)
		}
		wg.Wait()
		drops := make([]string, 0, n)
		for i := 0; i < n; i++ {
			drops = append(drops, fmt.Sprintf("drop-%d", i))
		}
		if err := s.SetRemoveCtx(ctx, "set-race", drops...); err != nil {
			t.Fatalf("SetRemoveCtx: %v", err)
		}
		want := make([]string, 0, n)
		for i := 0; i < n; i++ {
			want = append(want, fmt.Sprintf("keep-%d", i))
		}
		sort.Strings(want)
		if got := members(t, s, "set-race"); !equal(got, want) {
			t.Fatalf("lost updates: %d members, want %d", len(got), len(want))
		}
	})
}
