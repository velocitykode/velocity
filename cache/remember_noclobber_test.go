package cache_test

import (
	"context"
	"testing"
	"time"
)

// TestRemember_DoesNotClobberUserKeys proves the single-flight populate lock
// never touches ordinary user keys, including keys that share a prefix or
// substring with the populate target. Only the value slot ("target") and the
// dedicated lock key are written/forgotten.
//
// Scope: this covers ORDINARY user keys. It deliberately does NOT assert
// anything about a user value placed at the exact reserved lock key
// (rememberLockKey("target") == "\x00velocity/cache:remember-lock\x00target").
// That NUL-wrapped prefix is reserved framework-internal namespace (documented
// on rememberLockKeyPrefix in manager.go); a caller storing a value there is
// out of contract and may have it forgotten by the populater. Cache-key-based
// distributed locking cannot fully eliminate that reserved-key overlap, so it
// is reserved and documented rather than made collision-proof.
func TestRemember_DoesNotClobberUserKeys(t *testing.T) {
	for _, f := range allDriverFactories(t) {
		f := f
		t.Run(f.name, func(t *testing.T) {
			m, done := f.build(t)
			defer done()
			ctx := context.Background()

			// Seed ordinary user keys, some sharing a prefix/substring with
			// the populate target "target".
			seed := map[string]string{
				"target:meta":   "keep-meta",
				"other":         "keep-other",
				"prefix-target": "keep-prefix",
			}
			for k, v := range seed {
				if err := m.Put(k, v, time.Minute); err != nil {
					t.Fatalf("seed Put %q: %v", k, err)
				}
			}

			got, err := m.RememberEWithContext(ctx, "target", time.Minute, func() (interface{}, error) {
				return "computed", nil
			})
			if err != nil {
				t.Fatalf("Remember: %v", err)
			}
			if got != "computed" {
				t.Fatalf("Remember returned %v, want computed", got)
			}

			// Target now holds the computed value at its own slot.
			if v, ok := m.Get("target"); !ok || v != "computed" {
				t.Fatalf("target = %v ok=%v, want computed", v, ok)
			}

			// Every seeded user key is intact and uncorrupted.
			for k, v := range seed {
				gotV, ok := m.Get(k)
				if !ok {
					t.Fatalf("user key %q was clobbered by Remember (now missing)", k)
				}
				if gotV != v {
					t.Fatalf("user key %q corrupted by Remember: got %v, want %v", k, gotV, v)
				}
			}
		})
	}
}
