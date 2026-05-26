package scheduler

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// orderedAcquireLocker lets the test script different errors per
// Acquire key. Used to drive the rare pre-dispatch path where the
// OnOneServer acquire succeeds but the WithoutOverlapping acquire
// fails. Keys not in scriptedErrs fall back to a real
// InMemoryLocker so a successful Acquire still produces a real Lock
// the test can release.
type orderedAcquireLocker struct {
	scriptedErrs map[string]error
	delegate     Locker
}

func (l *orderedAcquireLocker) Acquire(ctx context.Context, key string, ttl time.Duration) (Lock, error) {
	if err, ok := l.scriptedErrs[key]; ok {
		return nil, err
	}
	return l.delegate.Acquire(ctx, key, ttl)
}

// TestRunDueJobs_OneServerLockReleasedOnOverlapAcquireFailure pins the
// invariant that a pre-dispatch failure on WithoutOverlapping does NOT
// leak the OnOneServer lock. The host has not run the job; holding
// the minute-keyed one-server slot would let a single host with a
// stale overlap lock suppress every other host for the rest of the
// minute.
//
// Setup: scripted locker that
//   - Lets OnOneServer.Acquire succeed via the delegate (returns a real Lock)
//   - Forces WithoutOverlapping.Acquire to fail with ErrLockHeld
//
// Post-fix: after runDueJobs returns the OnOneServer key must be
// re-acquirable from the same delegate (the scheduler released it on
// the failure path).
func TestRunDueJobs_OneServerLockReleasedOnOverlapAcquireFailure(t *testing.T) {
	t.Parallel()

	jobName := "lockleak.job"
	cron := fmt.Sprintf("%d * * * *", time.Now().Minute())

	delegate := NewInMemoryLocker()

	// Build the actual lock keys the scheduler will use so the
	// scripted locker can match them. The keys are stable: see
	// Job.oneServerLockKey / overlapLockKey.
	s := New()
	cfg := s.Named(jobName, func() {}).Cron(cron).OnOneServer().WithoutOverlapping()
	_ = cfg
	job := s.jobs[0]
	overlapKey := job.overlapLockKey()

	scripted := &orderedAcquireLocker{
		scriptedErrs: map[string]error{overlapKey: ErrLockHeld},
		delegate:     delegate,
	}
	s.SetLocker(scripted)

	s.runDueJobs()

	// Wait for any in-flight dispatch goroutines to finish so the
	// release side effects have landed.
	done := make(chan struct{})
	go func() { s.runWg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runWg leaked an Add on the pre-dispatch acquire-failure path")
	}

	// The minute-scoped OnOneServer key must be free; another host
	// in the cluster must be able to acquire it for this same minute
	// (e.g. retry after the failing host gave up).
	now := time.Now()
	osKey := job.oneServerLockKey(now)
	lk, err := delegate.Acquire(context.Background(), osKey, time.Minute)
	if err != nil {
		if errors.Is(err, ErrLockHeld) {
			t.Fatal("OnOneServer key leaked: another host cannot acquire after pre-dispatch overlap failure")
		}
		t.Fatalf("unexpected Acquire error: %v", err)
	}
	if lk == nil {
		t.Fatal("Acquire returned nil Lock without error")
	}
	_ = lk.Release(context.Background())
}
