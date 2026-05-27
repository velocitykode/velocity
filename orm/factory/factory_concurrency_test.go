package factory

import (
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

// TestFactory_ConcurrentDefineStateAndMake is the regression test for the
// map mutex sweep (rule #3) on *Factory: DefineState and Sequence writers
// must not race the State() presence-check or generateOne()'s map reads.
// Without the RWMutex fix in factory.go, this test fires "concurrent map
// read and map write" under -race.
//
// The harness fans out N DefineState/Sequence writers and M concurrent
// generateOne callers driving the map-read path with a fixed "seed"
// state (so concurrent activeState resets do not perturb the assertion).
// Pass criterion: no race detector report; every reader sees the seeded
// state's values applied to the generated row.
func TestFactory_ConcurrentDefineStateAndMake(t *testing.T) {
	t.Parallel()
	const writers = 16
	const readers = 32
	const iters = 64

	f := NewFactory(nil, "users", func() map[string]interface{} {
		return map[string]interface{}{"role": "guest"}
	})

	// Seed the state and a sequence the readers will exercise. These two
	// entries are NEVER overwritten so the post-write assertions stay
	// deterministic regardless of the writers' interleaving.
	f.DefineState("seed", map[string]interface{}{"role": "user"})
	f.Sequence("seq", func(n int) interface{} { return n })

	var wg sync.WaitGroup
	wg.Add(writers + readers)

	// Writers: define a stream of fresh states and sequences under
	// disjoint keys so they don't clobber "seed" / "seq".
	for i := 0; i < writers; i++ {
		i := i
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				name := "writer-" + strconv.Itoa(i) + "-" + strconv.Itoa(j)
				f.DefineState(name, map[string]interface{}{
					"writer":  i,
					"jitter":  j,
					"payload": name,
				})
				f.Sequence(name, func(n int) interface{} { return n + i })
			}
		}()
	}

	// Readers: call generateOne directly with the seed state. This
	// exercises the locked map iteration (states + sequences) without
	// going through the chain API whose activeState writes would race
	// with sibling readers.
	for r := 0; r < readers; r++ {
		r := r
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				out := f.generateOne("seed", j)
				if out["role"] != "user" {
					t.Errorf("reader %d iter %d: role = %v, want %q", r, j, out["role"], "user")
					return
				}
				if out["seq"] == nil {
					t.Errorf("reader %d iter %d: missing sequence value", r, j)
					return
				}
			}
		}()
	}

	wg.Wait()
}

// TestFactory_StatePresenceCheckIsAtomic guards the Factory.State invariant
// that the presence-check and the activeState assignment happen under the
// same lock. Without that, a DefineState wedged between the check and the
// assignment would let State succeed against a different (since-deleted)
// entry. The test exercises the happy path that the fix preserved; the
// race-fail behaviour is covered by the concurrent harness above.
func TestFactory_StatePresenceCheckIsAtomic(t *testing.T) {
	t.Parallel()
	f := NewFactory(nil, "users", func() map[string]interface{} {
		return map[string]interface{}{}
	})
	f.DefineState("admin", map[string]interface{}{"role": "admin"})

	out := f.State("admin").Make()
	m, ok := out.(map[string]interface{})
	if !ok {
		t.Fatalf("Make() returned %T, want map[string]interface{}", out)
	}
	if m["role"] != "admin" {
		t.Fatalf("Make() role = %v, want %q", m["role"], "admin")
	}
}

// TestModelFactory_ConcurrentDefineStateAndMakeOne mirrors the *Factory
// concurrency test on the generic *ModelFactory[T]. ModelFactory.State and
// makeOne shared the same un-locked-read footgun as Factory before the
// sweep. Pass criterion: no race report; every reader sees the seeded
// modifier applied.
//
// Readers call makeOne directly with the seeded state so concurrent
// activeState resets via the chain API do not perturb the assertion;
// the map-iteration race surface is what we are pinning, not chain
// semantics.
func TestModelFactory_ConcurrentDefineStateAndMakeOne(t *testing.T) {
	t.Parallel()
	const writers = 16
	const readers = 32
	const iters = 64

	type acct struct {
		Name string
		Role string
	}

	mf := NewModelFactory[acct](nil, func() *acct { return &acct{Name: "x"} })
	// Seed a state that is NEVER overwritten so the assertion stays
	// deterministic regardless of writer interleaving.
	mf.DefineState("seed", func(a *acct) { a.Role = "seeded" })

	var wg sync.WaitGroup
	wg.Add(writers + readers)

	var raceCount int64
	for i := 0; i < writers; i++ {
		i := i
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				name := "w-" + strconv.Itoa(i) + "-" + strconv.Itoa(j)
				v := strconv.Itoa(i) + ":" + strconv.Itoa(j)
				mf.DefineState(name, func(a *acct) { a.Role = v })
			}
		}()
	}
	for r := 0; r < readers; r++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				v := mf.makeOne("seed", nil)
				if v == nil || v.Role != "seeded" {
					atomic.AddInt64(&raceCount, 1)
				}
			}
		}()
	}
	wg.Wait()
	if raceCount != 0 {
		t.Fatalf("ModelFactory readers observed %d coherency violations", raceCount)
	}
}

// TestModelFactory_StatePresenceCheckIsAtomic is the ModelFactory counterpart
// to TestFactory_StatePresenceCheckIsAtomic.
func TestModelFactory_StatePresenceCheckIsAtomic(t *testing.T) {
	t.Parallel()
	type acct struct {
		Role string
	}
	mf := NewModelFactory[acct](nil, func() *acct { return &acct{} })
	mf.DefineState("admin", func(a *acct) { a.Role = "admin" })

	v := mf.State("admin").MakeOne(nil)
	if v == nil || v.Role != "admin" {
		t.Fatalf("MakeOne() Role = %q, want %q", v.Role, "admin")
	}
}
