package drivers

import (
	"context"
	"testing"
	"time"
)

// TestMemoryStore_CompareAndSwapCtx_ComparesExactStoredValue pins the memory
// store's swap equality: expected matches only a value reflect.DeepEqual to
// the one stored, the value a read returns. Values that collapse to the same
// JSON read shape (integers past float64 precision, an int and the float64
// of the same number) are different values here, because a memory read
// tells them apart.
func TestMemoryStore_CompareAndSwapCtx_ComparesExactStoredValue(t *testing.T) {
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
