package velocity

import (
	"sync/atomic"
	"testing"
	"time"
)

// A failure hook that reads the cache while a cache.hit listener fails
// causes a failing framework event of its own. The hook runs once for the
// original failure and is not re-entered for the one it caused, which is
// still counted.
func TestFailedEventHook_NotReenteredByTheFailuresItCauses(t *testing.T) {
	var a *App
	var calls atomic.Int32
	hook := func(error, any) {
		if calls.Add(1) > 5 {
			return // bound the recursion the base let through
		}
		a.Cache.Get("k")
	}
	a, _ = newLoggerWiringApp(t, nil, WithFailedEventHook(hook))
	a.Services.Events.Listen("cache.hit", failingListener{name: "cache"})
	if err := a.Cache.Put("k", "v", time.Minute); err != nil {
		t.Fatalf("put: %v", err)
	}
	before := a.FailedEventCount()

	if _, ok := a.Cache.Get("k"); !ok {
		t.Fatal("cache miss, want a hit")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("hook calls = %d, want 1 (not re-entered by the cache read it made)", got)
	}
	if got := a.FailedEventCount() - before; got != 2 {
		t.Errorf("FailedEventCount grew by %d, want 2 (the hit and the hook's hit)", got)
	}
}
