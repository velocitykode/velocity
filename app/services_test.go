package app

import (
	"sync"
	"testing"
	"time"
)

// thing is a minimal value used to exercise the component registry's
// concurrency and lock-discipline guarantees from inside the app package.
type thing struct{ name string }

// markerA / markerB are integrator-style qualifier markers letting the same
// concrete type register more than once under distinct keys.
type markerA struct{}
type markerB struct{}

// TestServices_ComponentsConcurrentRegisterAndRead is the regression test for
// the component-registry map mutex sweep (rule #3): RegisterFor, GetFor, and
// RangeComponents must be safe to call concurrently. Without the compMu
// protection in registry.go this fires "concurrent map read and map write"
// under -race.
//
// The typed registry keys on (type, qualifier), so the unique-key fan-out the
// old string API used is not expressible: writers instead contend on two
// qualifiers and a duplicate-key error is a valid concurrent outcome. The test
// passes when no operation races or panics and both qualifier entries are
// observable at the end.
func TestServices_ComponentsConcurrentRegisterAndRead(t *testing.T) {
	t.Parallel()
	const writers = 32
	const readersPerWriter = 4

	s := &Services{}

	var wg sync.WaitGroup
	wg.Add(writers + writers*readersPerWriter)

	for i := 0; i < writers; i++ {
		i := i
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				_ = RegisterFor[*thing, markerA](s, &thing{name: "a"})
			} else {
				_ = RegisterFor[*thing, markerB](s, &thing{name: "b"})
			}
		}()
		for j := 0; j < readersPerWriter; j++ {
			go func() {
				defer wg.Done()
				// Reads may or may not see the value depending on
				// scheduler order; the test is satisfied if no race
				// fires under the read path.
				_, _ = GetFor[*thing, markerA](s)
				s.RangeComponents(func(_ ComponentKey, _ any, _ []any) bool {
					return true
				})
			}()
		}
	}

	wg.Wait()

	if _, err := GetFor[*thing, markerA](s); err != nil {
		t.Fatalf("GetFor[markerA] post-fanout: %v", err)
	}
	if _, err := GetFor[*thing, markerB](s); err != nil {
		t.Fatalf("GetFor[markerB] post-fanout: %v", err)
	}
}

// TestServices_RegisterRejectsDuplicate pins the duplicate-key rejection
// invariant that Register promises: a second registration under the same
// (type, qualifier) key reports an error and the first registration wins.
func TestServices_RegisterRejectsDuplicate(t *testing.T) {
	t.Parallel()
	s := &Services{}
	first := &thing{name: "v1"}
	if err := Register(s, first); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := Register(s, &thing{name: "v2"}); err == nil {
		t.Fatal("second register: want error, got nil")
	}
	got, err := Get[*thing](s)
	if err != nil {
		t.Fatalf("Get after duplicate: %v", err)
	}
	if got != first {
		t.Fatal("Get after duplicate returned a new value; first registration must win")
	}
}

// TestServices_RangeComponentsEarlyStop verifies the documented contract that
// fn returning false halts iteration after exactly one visit. Two-key minimum
// to make the "saw exactly one" assertion meaningful.
func TestServices_RangeComponentsEarlyStop(t *testing.T) {
	t.Parallel()
	s := &Services{}
	if err := RegisterFor[*thing, markerA](s, &thing{}); err != nil {
		t.Fatalf("register A: %v", err)
	}
	if err := RegisterFor[*thing, markerB](s, &thing{}); err != nil {
		t.Fatalf("register B: %v", err)
	}
	seen := 0
	s.RangeComponents(func(_ ComponentKey, _ any, _ []any) bool {
		seen++
		return false
	})
	if seen != 1 {
		t.Fatalf("RangeComponents early stop: visited %d, want 1", seen)
	}
}

// TestServices_RangeComponentsDoesNotHoldLockAcrossFn is the F1 lock-discipline
// regression test (ported from the removed string-API range version):
// RangeComponents must NOT hold compMu across the user-supplied fn, because
// (a) a slow fn would block Register writers, and (b) fn calling back into
// Register would deadlock on re-entrant Lock against the held RLock.
//
// The harness pre-registers one component, then inside fn (a) acquires a write
// lock via Register on a fresh key and (b) reads the registry via Get. Both
// must succeed without deadlocking, under a 2-second watchdog.
func TestServices_RangeComponentsDoesNotHoldLockAcrossFn(t *testing.T) {
	t.Parallel()
	s := &Services{}
	if err := RegisterFor[*thing, markerA](s, &thing{name: "seed"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	done := make(chan struct{})
	go func() {
		s.RangeComponents(func(_ ComponentKey, _ any, _ []any) bool {
			if err := RegisterFor[*thing, markerB](s, &thing{name: "from-fn"}); err != nil {
				t.Errorf("register inside fn: %v", err)
			}
			if _, err := GetFor[*thing, markerA](s); err != nil {
				t.Errorf("read inside fn: %v", err)
			}
			return true
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RangeComponents held lock across fn; deadlock on re-entrant Register")
	}
	if _, err := GetFor[*thing, markerB](s); err != nil {
		t.Fatalf("post-Range registration not visible: %v", err)
	}
}
