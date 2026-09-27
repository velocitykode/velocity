//go:build unix

package drivers

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Many goroutines over two FileStore instances sharing one directory race
// Get/Release and Block on one lock key. A lock admits one holder at a
// time across instances: the number of callers inside the critical
// section never exceeds one.
func TestFileLock_CrossInstanceSingleHolder(t *testing.T) {
	stores := newSharedFileStores(t, 2)
	ctx := context.Background()

	var inside, maxInside, acquired atomic.Int32
	enter := func() {
		n := inside.Add(1)
		for {
			cur := maxInside.Load()
			if n <= cur || maxInside.CompareAndSwap(cur, n) {
				break
			}
		}
		acquired.Add(1)
		time.Sleep(50 * time.Microsecond)
		inside.Add(-1)
	}

	const goroutines = 16
	deadline := time.Now().Add(time.Second)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			s := stores[g%len(stores)]
			for i := 0; time.Now().Before(deadline); i++ {
				lock := s.Lock("shared", time.Minute)
				if i%4 == 0 {
					_ = lock.Block(ctx, 50*time.Millisecond, enter)
					continue
				}
				if lock.Get(ctx) {
					enter()
					if !lock.Release(ctx) {
						t.Error("Release by the holder returned false")
						return
					}
				}
			}
		}(g)
	}
	wg.Wait()
	if acquired.Load() == 0 {
		t.Fatal("no caller acquired the lock")
	}
	if got := maxInside.Load(); got > 1 {
		t.Fatalf("lock held by %d callers at once; want at most 1", got)
	}
}

// A caller of instance 2 opens the lock's file before the holder on
// instance 1 releases, and finishes acquiring after the release; a caller
// of instance 3 acquires in between. Only one of the two may hold the
// lock. Unlinking the lock file on release let instance 2 lock the old
// file while instance 3 locked a new one at the same path.
func TestFileLock_ReleaseDuringPeerAcquireKeepsSingleHolder(t *testing.T) {
	stores := newSharedFileStores(t, 3)
	ctx := context.Background()

	holder := stores[0].Lock("stepped", time.Minute)
	if !holder.Get(ctx) {
		t.Fatal("instance 1 Get failed")
	}

	opened, resumeOpened := make(chan struct{}), make(chan struct{})
	read, resumeRead := make(chan struct{}), make(chan struct{})
	var openedOnce, readOnce sync.Once
	stores[1].lockStepHook = func(step string) {
		switch step {
		case "guard-opened":
			openedOnce.Do(func() { close(opened); <-resumeOpened })
		case "record-read":
			readOnce.Do(func() { close(read); <-resumeRead })
		}
	}

	second := make(chan bool, 1)
	go func() { second <- stores[1].Lock("stepped", time.Minute).Get(ctx) }()
	<-opened

	if !holder.Release(ctx) {
		t.Fatal("instance 1 Release failed")
	}
	close(resumeOpened)
	<-read

	third := make(chan bool, 1)
	go func() { third <- stores[2].Lock("stepped", time.Minute).Get(ctx) }()
	var thirdGot, thirdDone bool
	select {
	case thirdGot = <-third:
		thirdDone = true
	case <-time.After(300 * time.Millisecond):
		// Waiting for instance 2's acquire to finish.
	}
	close(resumeRead)
	secondGot := <-second
	if !thirdDone {
		thirdGot = <-third
	}
	if secondGot && thirdGot {
		t.Fatal("instances 2 and 3 both hold the lock")
	}
	if !secondGot && !thirdGot {
		t.Fatal("neither instance 2 nor instance 3 acquired the released lock")
	}
}

// An expired entry taken over by AddCtx from goroutines of two instances
// sharing the directory has exactly one winner per round.
func TestFileStore_AddTakeover_CrossInstanceSingleWinner(t *testing.T) {
	stores := newSharedFileStores(t, 2)
	ctx := context.Background()

	const rounds, perStore = 20, 8
	for r := 0; r < rounds; r++ {
		key := fmt.Sprintf("takeover-%d", r)
		writeExpiredFile(t, stores[0], key, "stale")

		var wins atomic.Int32
		var wg sync.WaitGroup
		for g := 0; g < 2*perStore; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				ok, err := stores[g%2].AddCtx(ctx, key, g, time.Hour)
				if err != nil {
					t.Errorf("round %d: AddCtx: %v", r, err)
					return
				}
				if ok {
					wins.Add(1)
				}
			}(g)
		}
		wg.Wait()
		if got := wins.Load(); got != 1 {
			t.Fatalf("round %d: %d AddCtx callers took over the expired entry; want 1", r, got)
		}
	}
}

// A lock whose TTL has passed is free for the next caller, as on the
// memory and redis drivers, and its former holder can no longer release
// the new holder's lock.
func TestFileLock_ExpiredLockIsReacquirable(t *testing.T) {
	stores := newSharedFileStores(t, 2)
	ctx := context.Background()

	first := stores[0].Lock("ttl", 50*time.Millisecond)
	if !first.Get(ctx) {
		t.Fatal("first Get failed")
	}
	if stores[1].Lock("ttl", time.Minute).Get(ctx) {
		t.Fatal("second Get acquired a lock inside its TTL")
	}
	time.Sleep(100 * time.Millisecond)

	second := stores[1].Lock("ttl", time.Minute)
	if !second.Get(ctx) {
		t.Fatal("Get after the TTL passed failed")
	}
	if first.Release(ctx) {
		t.Fatal("expired holder released the new holder's lock")
	}
	if stores[0].Lock("ttl", time.Minute).Get(ctx) {
		t.Fatal("Get acquired a lock the second holder still holds")
	}
	if !second.Release(ctx) {
		t.Fatal("second holder Release failed")
	}
}
