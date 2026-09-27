//go:build unix

package drivers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newSharedFileStores(t *testing.T, n int) []*FileStore {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "shared-cache")
	stores := make([]*FileStore, n)
	for i := range stores {
		s, err := NewFileStoreWithOptions("swap", dir, time.Hour)
		if err != nil {
			t.Fatalf("NewFileStoreWithOptions: %v", err)
		}
		t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
		stores[i] = s
	}
	return stores
}

// Many goroutines swapping one key from the same expected value: exactly
// one wins each round, and the winners' writes chain without a gap.
func TestFileStore_CompareAndSwap_ConcurrentOneWinnerPerValue(t *testing.T) {
	t.Parallel()
	s := newSharedFileStores(t, 1)[0]
	ctx := context.Background()
	if err := s.PutCtx(ctx, "counter", "v0", time.Hour); err != nil {
		t.Fatalf("PutCtx: %v", err)
	}
	const rounds, goroutines = 20, 16
	for r := 0; r < rounds; r++ {
		expected, next := fmt.Sprintf("v%d", r), fmt.Sprintf("v%d", r+1)
		var winners atomic.Int32
		var wg sync.WaitGroup
		for g := 0; g < goroutines; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ok, err := s.CompareAndSwapCtx(ctx, "counter", expected, next, time.Hour)
				if err != nil {
					t.Errorf("CompareAndSwapCtx: %v", err)
					return
				}
				if ok {
					winners.Add(1)
				}
			}()
		}
		wg.Wait()
		if got := winners.Load(); got != 1 {
			t.Fatalf("round %d: %d winners, want exactly 1", r, got)
		}
		if v, _ := s.GetCtx(ctx, "counter"); v != next {
			t.Fatalf("round %d: value %v, want %v", r, v, next)
		}
	}
}

// Two FileStore instances over one directory (two processes sharing a
// cache path): both can never win the same swap, and a read-swap loop on
// each loses no increment.
func TestFileStore_CompareAndSwap_CrossInstance(t *testing.T) {
	t.Parallel()
	stores := newSharedFileStores(t, 2)
	ctx := context.Background()

	for i := 0; i < 30; i++ {
		key := fmt.Sprintf("xswap-%d", i)
		if err := stores[0].PutCtx(ctx, key, "start", time.Hour); err != nil {
			t.Fatalf("PutCtx: %v", err)
		}
		var oks [2]bool
		var wg sync.WaitGroup
		for j, s := range stores {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ok, err := s.CompareAndSwapCtx(ctx, key, "start", fmt.Sprintf("from-%d", j), time.Hour)
				if err != nil {
					t.Errorf("CompareAndSwapCtx: %v", err)
				}
				oks[j] = ok
			}()
		}
		wg.Wait()
		if oks[0] == oks[1] {
			t.Fatalf("iter %d: both or neither instance won the swap: %v", i, oks)
		}
	}

	// Read-modify-swap increments from both instances: no update lost.
	if err := stores[0].PutCtx(ctx, "tally", float64(0), time.Hour); err != nil {
		t.Fatalf("PutCtx: %v", err)
	}
	const perInstance = 40
	var wg sync.WaitGroup
	for _, s := range stores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < perInstance; {
				v, ok := s.GetCtx(ctx, "tally")
				if !ok {
					t.Errorf("tally missing")
					return
				}
				swapped, err := s.CompareAndSwapCtx(ctx, "tally", v, v.(float64)+1, time.Hour)
				if err != nil {
					t.Errorf("CompareAndSwapCtx: %v", err)
					return
				}
				if swapped {
					n++
				}
			}
		}()
	}
	wg.Wait()
	if v, _ := stores[1].GetCtx(ctx, "tally"); v != float64(2*perInstance) {
		t.Fatalf("tally = %v, want %d", v, 2*perInstance)
	}
}

// A key forgotten through another instance is never recreated by a swap.
func TestFileStore_CompareAndSwap_AfterForgetOnOtherInstance(t *testing.T) {
	t.Parallel()
	stores := newSharedFileStores(t, 2)
	ctx := context.Background()
	_ = stores[0].PutCtx(ctx, "gone", "v", time.Hour)
	if err := stores[1].ForgetCtx(ctx, "gone"); err != nil {
		t.Fatalf("ForgetCtx: %v", err)
	}
	ok, err := stores[0].CompareAndSwapCtx(ctx, "gone", "v", "again", time.Hour)
	if err != nil || ok {
		t.Fatalf("CompareAndSwapCtx = (%v, %v), want (false, nil)", ok, err)
	}
	if _, found := stores[1].GetCtx(ctx, "gone"); found {
		t.Fatal("a swap recreated a forgotten key")
	}
}

// Set updates from two instances over one directory are all kept.
func TestFileStore_SetAdd_CrossInstanceNoLostUpdates(t *testing.T) {
	t.Parallel()
	stores := newSharedFileStores(t, 2)
	ctx := context.Background()
	const perInstance = 40
	var wg sync.WaitGroup
	for j, s := range stores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < perInstance; n++ {
				if err := s.SetAddCtx(ctx, "members", time.Hour, fmt.Sprintf("keep-%d-%d", j, n), fmt.Sprintf("drop-%d-%d", j, n)); err != nil {
					t.Errorf("SetAddCtx: %v", err)
					return
				}
				if err := s.SetRemoveCtx(ctx, "members", fmt.Sprintf("drop-%d-%d", j, n)); err != nil {
					t.Errorf("SetRemoveCtx: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	got, err := stores[0].SetMembersCtx(ctx, "members")
	if err != nil {
		t.Fatalf("SetMembersCtx: %v", err)
	}
	var want []string
	for j := range stores {
		for n := 0; n < perInstance; n++ {
			want = append(want, fmt.Sprintf("keep-%d-%d", j, n))
		}
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("members: got %d, want %d (%v)", len(got), len(want), got)
	}
}

// A set key reads back as its member map, as on the memory driver, and is
// neither a string nor a number.
func TestFileStore_SetKey_ReadsAsMemberMap(t *testing.T) {
	t.Parallel()
	s := newSharedFileStores(t, 1)[0]
	ctx := context.Background()
	if err := s.SetAddCtx(ctx, "set", time.Hour, "b", "a"); err != nil {
		t.Fatalf("SetAddCtx: %v", err)
	}
	v, found := s.GetCtx(ctx, "set")
	if !found || !reflect.DeepEqual(v, map[string]struct{}{"a": {}, "b": {}}) {
		t.Fatalf("GetCtx = (%v, %v), want the member map", v, found)
	}
	if _, ok := s.GetStringCtx(ctx, "set"); ok {
		t.Fatal("GetStringCtx read a set as a string")
	}
	if _, err := s.IncrementCtx(ctx, "set", 1); err == nil {
		t.Fatal("IncrementCtx treated a set as a number")
	}
	// A value never reads as a set.
	_ = s.PutCtx(ctx, "value", map[string]any{"members": []string{"x"}}, time.Hour)
	if got, _ := s.SetMembersCtx(ctx, "value"); got != nil {
		t.Fatalf("SetMembersCtx read a value as a set: %v", got)
	}
	// A swap against the set compares the member map a read returned.
	ok, err := s.CompareAndSwapCtx(ctx, "set", v, "replaced", time.Hour)
	if err != nil || !ok {
		t.Fatalf("CompareAndSwapCtx on the read of a set = (%v, %v)", ok, err)
	}
}

// A swap waits for a held key write lock instead of reporting a value it
// never compared, and gives up with the context.
func TestFileStore_CompareAndSwap_WaitsForHeldKeyLock(t *testing.T) {
	t.Parallel()
	stores := newSharedFileStores(t, 2)
	ctx := context.Background()
	_ = stores[0].PutCtx(ctx, "held", "v", time.Hour)

	unlock, err := stores[1].lockKeyForWrite(ctx, "held")
	if err != nil {
		t.Fatalf("lockKeyForWrite: %v", err)
	}
	type result struct {
		ok  bool
		err error
	}
	done := make(chan result, 1)
	go func() {
		ok, err := stores[0].CompareAndSwapCtx(ctx, "held", "v", "next", time.Hour)
		done <- result{ok, err}
	}()
	select {
	case r := <-done:
		t.Fatalf("swap returned (%v, %v) while another instance held the key lock", r.ok, r.err)
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	r := <-done
	if r.err != nil || !r.ok {
		t.Fatalf("swap after the lock was released = (%v, %v), want (true, nil)", r.ok, r.err)
	}

	unlock, err = stores[1].lockKeyForWrite(ctx, "held")
	if err != nil {
		t.Fatalf("lockKeyForWrite: %v", err)
	}
	defer unlock()
	cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	ok, err := stores[0].CompareAndSwapCtx(cctx, "held", "next", "late", time.Hour)
	if ok || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("swap on a held lock with an expiring context = (%v, %v), want DeadlineExceeded", ok, err)
	}
}

// The key write-lock files are never removed: Flush and the expiry sweep
// skip them, so every holder keeps locking the same file.
func TestFileStore_KeyLockFilesSurviveFlushAndSweep(t *testing.T) {
	t.Parallel()
	s := newSharedFileStores(t, 1)[0]
	ctx := context.Background()
	if err := s.PutCtx(ctx, "k", "v", time.Hour); err != nil {
		t.Fatalf("PutCtx: %v", err)
	}
	entries, err := os.ReadDir(s.keyLockDir())
	if err != nil || len(entries) == 0 {
		t.Fatalf("no key lock file after a write: %v", err)
	}
	lockFile := filepath.Join(s.keyLockDir(), entries[0].Name())
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(lockFile, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	s.sweepExpired()
	if _, err := os.Stat(lockFile); err != nil {
		t.Fatalf("the expiry sweep removed a key lock file: %v", err)
	}
	if err := s.FlushCtx(ctx); err != nil {
		t.Fatalf("FlushCtx: %v", err)
	}
	if _, err := os.Stat(lockFile); err != nil {
		t.Fatalf("Flush removed a key lock file: %v", err)
	}
	if _, found := s.GetCtx(ctx, "k"); found {
		t.Fatal("Flush left the entry")
	}
}
