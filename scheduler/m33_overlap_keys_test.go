package scheduler

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestOverlapAndOneServerKeysDoNotCollide pins the cluster-wide
// invariant for M-33: the WithoutOverlapping and OnOneServer guards must
// use disjoint lock-key namespaces. If they collided, a long-running
// WithoutOverlapping job on host A could starve the next minute's
// OnOneServer contest on host B (or vice versa). The two prefixes are
// part of the on-the-wire protocol the cluster shares, so any future
// rename must be deliberate.
func TestOverlapAndOneServerKeysDoNotCollide(t *testing.T) {
	t.Parallel()

	j := &Job{name: "billing.run", schedule: &Schedule{}, timezone: time.UTC}
	overlapKey := j.overlapLockKey()
	oneServerKey := j.oneServerLockKey(time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC))

	if overlapKey == oneServerKey {
		t.Fatalf("overlap and oneserver keys must differ, both = %q", overlapKey)
	}
	if !strings.HasPrefix(overlapKey, "velocity/scheduler/overlap:") {
		t.Errorf("overlapLockKey prefix changed: %q", overlapKey)
	}
	if !strings.HasPrefix(oneServerKey, "velocity/scheduler/oneserver:") {
		t.Errorf("oneServerLockKey prefix changed: %q", oneServerKey)
	}

	// The shared Locker must accept both keys concurrently, even with
	// identical job name. A job flagged with both gates exists in the
	// production gallery (see m33_combined test below).
	loc := NewInMemoryLocker()
	if _, err := loc.Acquire(context.Background(), overlapKey, time.Hour); err != nil {
		t.Fatalf("acquire overlap: %v", err)
	}
	if _, err := loc.Acquire(context.Background(), oneServerKey, time.Hour); err != nil {
		t.Fatalf("acquire oneserver must succeed alongside overlap, got: %v", err)
	}
}

// TestRunDueJobs_BothGates_ExactlyOnce covers M-33's combined-gate path:
// a job flagged with both OnOneServer and WithoutOverlapping must still
// run exactly once across N hosts at a tick boundary. Both gates fire
// against the shared Locker; failure of either skips the dispatch.
func TestRunDueJobs_BothGates_ExactlyOnce(t *testing.T) {
	t.Parallel()

	shared := NewInMemoryLocker()
	cron := fmt.Sprintf("%d * * * *", time.Now().Minute())

	var counter atomic.Int32
	work := func() { counter.Add(1) }

	hostA := New()
	hostA.SetLocker(shared)
	hostA.Named("nightly.report", work).Cron(cron).OnOneServer().WithoutOverlapping()

	hostB := New()
	hostB.SetLocker(shared)
	hostB.Named("nightly.report", work).Cron(cron).OnOneServer().WithoutOverlapping()

	hostA.runDueJobs()
	hostB.runDueJobs()

	hostA.runWg.Wait()
	hostB.runWg.Wait()

	if got := counter.Load(); got != 1 {
		t.Fatalf("expected exactly 1 execution under combined OnOneServer+WithoutOverlapping; got %d", got)
	}
}
