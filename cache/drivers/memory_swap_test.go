package drivers

import (
	"context"
	"math"
	"testing"
	"time"
)

// TestMemoryStore_CompareAndSwapCtx_ComparesExactStoredValue pins the memory
// store's swap equality: expected matches only a value reflect.DeepEqual to
// the one stored, the value a read returns. Values that collapse to the same
// JSON read shape (integers past float64 precision, an int and the float64
// of the same number) are different values here, because a memory read
// tells them apart.
//
// Values that do not equal themselves are outside the comparison domain
// (contract.CacheSwapper): a NaN, a non-nil func, and a copy of a value
// holding a NaN never match; the value a read returned holding a NaN in a
// map does, since that is the stored map itself, and so does a new struct
// or slice around that same map, since the identity shortcut applies at
// every depth.
func TestMemoryStore_CompareAndSwapCtx_ComparesExactStoredValue(t *testing.T) {
	fn := func() {}
	holdsNaN := map[string]any{"x": math.NaN()}
	type wraps struct{ M map[string]any }
	cases := []struct {
		name             string
		stored, expected any
		wantSwap         bool
	}{
		{"StaleLargeInteger", uint64(1<<53 + 1), uint64(1 << 53), false},
		{"StaleLargeIntegerInMap", map[string]any{"n": uint64(1<<53 + 1)}, map[string]any{"n": uint64(1 << 53)}, false},
		{"StaleLargeIntegerInSlice", []any{uint64(1<<53 + 1)}, []any{uint64(1 << 53)}, false},
		{"SameNumberOtherType", 5, float64(5), false},
		{"UnchangedLargeInteger", uint64(1<<53 + 1), uint64(1<<53 + 1), true},
		{"UnchangedNestedValue", map[string]any{"n": []any{uint64(1<<53 + 1)}}, map[string]any{"n": []any{uint64(1<<53 + 1)}}, true},
		{"UnchangedNaN", math.NaN(), math.NaN(), false},
		{"UnchangedFunc", fn, fn, false},
		{"CopyOfMapHoldingNaN", holdsNaN, map[string]any{"x": math.NaN()}, false},
		{"SameMapHoldingNaN", holdsNaN, holdsNaN, true},
		{"NewStructAroundSameMapHoldingNaN", wraps{M: holdsNaN}, wraps{M: holdsNaN}, true},
		{"NewSliceAroundSameMapHoldingNaN", []any{holdsNaN}, []any{holdsNaN}, true},
		{"NewStructAroundCopyOfMapHoldingNaN", wraps{M: holdsNaN}, wraps{M: map[string]any{"x": math.NaN()}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewMemoryStore("swap-exact")
			t.Cleanup(func() { _ = s.Shutdown(t.Context()) })
			ctx := context.Background()
			if err := s.PutCtx(ctx, "k", tc.stored, time.Minute); err != nil {
				t.Fatalf("PutCtx: %v", err)
			}
			ok, err := s.CompareAndSwapCtx(ctx, "k", tc.expected, "next", time.Minute)
			if err != nil {
				t.Fatalf("CompareAndSwapCtx: %v", err)
			}
			if ok != tc.wantSwap {
				t.Fatalf("CompareAndSwapCtx swapped=%v, want %v", ok, tc.wantSwap)
			}
		})
	}
}
