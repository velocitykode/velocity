package velocity

import (
	"context"
	"testing"
	"time"

	"github.com/velocitykode/velocity/cache"
)

// afterCommitFailingListener waits for the surrounding transaction to
// commit and then fails.
type afterCommitFailingListener struct{ failingListener }

func (afterCommitFailingListener) ShouldDispatchAfterCommit() bool { return true }

// hookCalls returns how many times the hook h records was called.
func hookCalls(h *hookRecorder) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.events)
}

// A framework event dispatched inside a transaction to listeners that wait
// for the commit and then fail is one failed delivery: counted once,
// logged once and handed to the hook once, however many of them fail, and
// the transaction still returns their failure.
func TestAfterCommitListenerFailures_ReachTheFailurePolicyOnce(t *testing.T) {
	var rec hookRecorder
	a, capture := newLoggerWiringApp(t, func(c *Config) {
		c.DB = DBConfig{Connection: "sqlite", Database: ":memory:"}
	}, WithFailedEventHook(rec.hook))
	a.Services.Events.Listen("cache.hit", afterCommitFailingListener{failingListener{name: "cache.hit"}})
	a.Services.Events.Listen("cache.hit", afterCommitFailingListener{failingListener{name: "cache.hit"}})
	if err := a.Cache.Put("k", "v", time.Minute); err != nil {
		t.Fatalf("put: %v", err)
	}
	mgr, ok := a.Cache.(*cache.Manager)
	if !ok {
		t.Fatalf("a.Cache = %T, want *cache.Manager", a.Cache)
	}

	err := a.DB.Transaction(context.Background(), func(ctx context.Context) error {
		if _, ok := mgr.GetWithContext(ctx, "k"); !ok {
			t.Fatal("cache miss, want a hit")
		}
		return nil
	})
	if err == nil {
		t.Fatal("transaction returned nil, want the after-commit listeners' failure")
	}
	if got := a.FailedEventCount(); got != 1 {
		t.Errorf("FailedEventCount = %d, want 1", got)
	}
	if got := hookCalls(&rec); got != 1 {
		t.Errorf("hook calls = %d, want 1", got)
	}
	if got := eventFailureWarns(capture, "cache.hit"); got != 1 {
		t.Errorf("warn lines for cache.hit = %d, want 1", got)
	}
}
