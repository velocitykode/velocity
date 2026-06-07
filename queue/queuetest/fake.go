package queuetest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/queue"
)

// pushedJob records a single job push: the job itself, the resolved queue
// name it landed on, any delay requested via PushDelayedCtx, and the time at
// which the job becomes poppable. A zero readyAt means immediately ready.
type pushedJob struct {
	job     contract.QueueJob
	queue   string
	delay   time.Duration
	readyAt time.Time
}

// ready reports whether the job is currently visible (not held back by a delay).
func (p pushedJob) ready(now time.Time) bool {
	return p.readyAt.IsZero() || !now.Before(p.readyAt)
}

// FakeQueue is an in-memory test double for contract.QueueDriver. It records
// every pushed job so tests can assert on dispatch behavior without a real
// backing store. All access to the recorded slice is mutex-guarded; getters
// return copies so callers cannot race on internal state.
type FakeQueue struct {
	mu     sync.Mutex
	pushed []pushedJob
}

// NewFakeQueue creates a new fake queue driver.
func NewFakeQueue() *FakeQueue {
	return &FakeQueue{}
}

var _ contract.QueueDriver = (*FakeQueue)(nil)

// PushCtx records a job on the resolved queue (defaulting to "default").
// A cancelled ctx aborts the push before it is stored.
func (f *FakeQueue) PushCtx(ctx context.Context, job contract.QueueJob, queueName ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pushed = append(f.pushed, pushedJob{job: job, queue: queue.ResolveQueueName(job, queueName...)})
	return nil
}

// PushDelayedCtx records a job along with its delay on the resolved queue.
// The job is not poppable until the delay elapses. A cancelled ctx aborts the
// push before it is stored.
func (f *FakeQueue) PushDelayedCtx(ctx context.Context, job contract.QueueJob, delay time.Duration, queueName ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var readyAt time.Time
	if delay > 0 {
		readyAt = time.Now().Add(delay)
	}
	f.pushed = append(f.pushed, pushedJob{job: job, queue: queue.ResolveQueueName(job, queueName...), delay: delay, readyAt: readyAt})
	return nil
}

// PopCtx removes and returns the next ready job for the queue, or (nil, nil)
// when no job is ready. Jobs whose delay has not elapsed are skipped. A
// cancelled ctx returns its error without mutating state.
func (f *FakeQueue) PopCtx(ctx context.Context, queue string) (contract.QueueJob, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	for i, p := range f.pushed {
		if p.queue == queue && p.ready(now) {
			f.pushed = append(f.pushed[:i], f.pushed[i+1:]...)
			return p.job, nil
		}
	}
	return nil, nil
}

// Size returns the number of ready (visible) jobs for the queue. Jobs whose
// delay has not yet elapsed are not counted.
func (f *FakeQueue) Size(queue string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	var n int64
	for _, p := range f.pushed {
		if p.queue == queue && p.ready(now) {
			n++
		}
	}
	return n, nil
}

// Clear drops all recorded jobs for the queue.
func (f *FakeQueue) Clear(queue string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := f.pushed[:0]
	for _, p := range f.pushed {
		if p.queue != queue {
			kept = append(kept, p)
		}
	}
	f.pushed = kept
	return nil
}

// Failed is a no-op record; the fake does not track failures.
func (f *FakeQueue) Failed(job contract.QueueJob, err error, queue string) error {
	return nil
}

// Shutdown is a no-op.
func (f *FakeQueue) Shutdown(ctx context.Context) error {
	return nil
}

// AssertPushed fails the test if no recorded job satisfies match.
func (f *FakeQueue) AssertPushed(t *testing.T, match func(contract.QueueJob) bool) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.pushed {
		if match(p.job) {
			return
		}
	}
	t.Errorf("expected a matching job to be pushed, but none was")
}

// AssertPushedOn fails the test if no recorded job on the given queue
// satisfies match.
func (f *FakeQueue) AssertPushedOn(t *testing.T, queue string, match func(contract.QueueJob) bool) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.pushed {
		if p.queue == queue && match(p.job) {
			return
		}
	}
	t.Errorf("expected a matching job to be pushed on queue %q, but none was", queue)
}

// AssertNotPushed fails the test if any recorded job satisfies match.
func (f *FakeQueue) AssertNotPushed(t *testing.T, match func(contract.QueueJob) bool) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.pushed {
		if match(p.job) {
			t.Errorf("expected no matching job to be pushed, but one was")
			return
		}
	}
}

// AssertNothingPushed fails the test if any job was recorded.
func (f *FakeQueue) AssertNothingPushed(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pushed) > 0 {
		t.Errorf("%d jobs were pushed but none were expected", len(f.pushed))
	}
}

// GetPushed returns a copy of all recorded jobs in push order.
func (f *FakeQueue) GetPushed() []contract.QueueJob {
	f.mu.Lock()
	defer f.mu.Unlock()
	jobs := make([]contract.QueueJob, len(f.pushed))
	for i, p := range f.pushed {
		jobs[i] = p.job
	}
	return jobs
}
