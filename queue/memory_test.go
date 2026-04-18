package queue

import (
	"container/heap"
	"context"
	"testing"
	"time"
)

// TestDelayedHeap_OrderByRunAt verifies the min-heap invariant: Pop always
// returns the entry with the earliest runAt regardless of push order. This
// is the correctness contract the O(log n) rewrite of moveReadyJobs relies on.
func TestDelayedHeap_OrderByRunAt(t *testing.T) {
	h := &delayedHeap{}
	now := time.Now()

	// Push out-of-order runAt values.
	runAts := []time.Time{
		now.Add(5 * time.Second),
		now.Add(1 * time.Second),
		now.Add(10 * time.Second),
		now.Add(500 * time.Millisecond),
		now.Add(3 * time.Second),
	}
	for _, ra := range runAts {
		heap.Push(h, &delayedJob{runAt: ra})
	}

	// Pop everything; readings must come out sorted ascending.
	var got []time.Time
	for h.Len() > 0 {
		got = append(got, heap.Pop(h).(*delayedJob).runAt)
	}

	for i := 1; i < len(got); i++ {
		if got[i].Before(got[i-1]) {
			t.Fatalf("heap violated order at index %d: %v before %v", i, got[i], got[i-1])
		}
	}
}

// TestMemoryDriver_MoveReadyJobs_HeapOrdering verifies the public behaviour:
// jobs pushed with different delays become available in readyAt order when
// the background sweeper runs.
func TestMemoryDriver_MoveReadyJobs_HeapOrdering(t *testing.T) {
	d := NewMemoryDriver()
	// Don't Start() so we can call moveReadyJobs deterministically.

	// Push three jobs with delays that are already in the past so they
	// are all ready on the next sweep.
	want := []string{"first", "second", "third"}

	// Insert out-of-order: runAt descending ensures the heap reorders.
	for i, id := range []string{want[2], want[0], want[1]} {
		offset := time.Duration(i) * time.Millisecond
		_ = d.PushDelayedCtx(context.Background(), &TestJob{ID: id}, -time.Second+offset, "queue")
	}

	// Rewrite each delayedJob's runAt to a known deterministic value so
	// ordering is independent of the millisecond-level skew PushDelayed
	// introduces. Directly mutating the heap here is safe because we have
	// exclusive access before Start() has been called.
	d.mu.Lock()
	h := d.delayed["queue"]
	if h == nil || h.Len() != 3 {
		d.mu.Unlock()
		t.Fatalf("expected 3 delayed jobs, got %v", h)
	}
	// Map id -> target runAt with early readyAt for "first".
	target := map[string]time.Time{
		"first":  time.Now().Add(-3 * time.Second),
		"second": time.Now().Add(-2 * time.Second),
		"third":  time.Now().Add(-1 * time.Second),
	}
	for _, j := range h.items {
		j.runAt = target[j.wrapper.Job.(*TestJob).ID]
	}
	heap.Init(h)
	d.mu.Unlock()

	// Sweep promotes ready jobs in heap (readyAt) order.
	d.moveReadyJobs()

	d.mu.Lock()
	q := d.queues["queue"]
	if q == nil {
		d.mu.Unlock()
		t.Fatal("no main queue for 'queue'")
	}
	var got []string
	for e := q.Front(); e != nil; e = e.Next() {
		got = append(got, e.Value.(*JobWrapper).Job.(*TestJob).ID)
	}
	d.mu.Unlock()

	if len(got) != len(want) {
		t.Fatalf("got %d jobs, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i] != id {
			t.Errorf("order mismatch at %d: got %q, want %q (all=%v)", i, got[i], id, got)
		}
	}
}

// TestMemoryDriver_MoveReadyJobs_PartiallyReady asserts jobs with readyAt in
// the future stay in the heap while ready ones are promoted.
func TestMemoryDriver_MoveReadyJobs_PartiallyReady(t *testing.T) {
	d := NewMemoryDriver()

	_ = d.PushDelayedCtx(context.Background(), &TestJob{ID: "ready-1"}, -time.Second, "q")
	_ = d.PushDelayedCtx(context.Background(), &TestJob{ID: "future-1"}, time.Hour, "q")
	_ = d.PushDelayedCtx(context.Background(), &TestJob{ID: "ready-2"}, -2*time.Second, "q")

	d.moveReadyJobs()

	d.mu.Lock()
	q := d.queues["q"]
	if q == nil || q.Len() != 2 {
		d.mu.Unlock()
		t.Fatalf("expected 2 ready jobs promoted, got %v", q)
	}

	h := d.delayed["q"]
	if h == nil || h.Len() != 1 {
		d.mu.Unlock()
		t.Fatalf("expected 1 future job still delayed, got %v", h)
	}
	remaining := h.peek()
	d.mu.Unlock()
	if remaining.wrapper.Job.(*TestJob).ID != "future-1" {
		t.Errorf("expected 'future-1' to remain delayed, got %q", remaining.wrapper.Job.(*TestJob).ID)
	}
}
