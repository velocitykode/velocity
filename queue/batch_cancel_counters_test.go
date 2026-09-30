package queue

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// Cancelling a batch while its jobs record failures loses none of them.
// Without AllowFailures every failing job cancels the batch, so the
// cancellation races the other jobs' outcomes; every failure stays counted
// and the batch ends with no pending slot. Run under -race.
func TestBatch_CancelKeepsConcurrentOutcomes(t *testing.T) {
	resetBatchStoreForTest(t)
	const rounds, jobs = 300, 16
	for range rounds {
		list := make([]Job, jobs)
		for j := range list {
			list[j] = &testBatchJob{}
		}
		b, err := NewBatch(list...).Dispatch(context.Background(), newMemoryDriver())
		if err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		for range jobs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				b.recordFailure(context.Background(), errors.New("job broke"))
			}()
		}
		close(start)
		wg.Wait()
		if got, pending := b.FailedJobs(), b.PendingJobs(); got != jobs || pending != 0 {
			t.Fatalf("FailedJobs = %d, PendingJobs = %d; want %d, 0", got, pending, jobs)
		}
		if !b.Finished() {
			t.Fatal("batch not finished after every job failed")
		}
	}
}

// readbackRepo stands in for a repository whose readbacks are copies of
// the stored batch, as the database repository's are. Its Cancel returns
// the readback taken before the latest counter change: a readback that
// lost the race with a concurrent job's mirror, as the database
// repository's Cancel (an UPDATE, then a separate Find) can.
type readbackRepo struct {
	BatchRepository
	mu     sync.Mutex
	stored map[BatchID]*Batch
	prev   map[BatchID]*Batch
}

func newReadbackRepo() *readbackRepo {
	return &readbackRepo{
		BatchRepository: NewInMemoryBatchRepository(),
		stored:          map[BatchID]*Batch{},
		prev:            map[BatchID]*Batch{},
	}
}

func readback(b *Batch) *Batch {
	c := &Batch{id: b.id, totalJobs: b.totalJobs, allowFailures: b.allowFailures, queue: b.queue}
	c.pendingJobs.Store(b.pendingJobs.Load())
	c.completedJobs.Store(b.completedJobs.Load())
	c.failedJobs.Store(b.failedJobs.Load())
	c.cancelled.Store(b.cancelled.Load())
	c.finished.Store(b.finished.Load())
	return c
}

func (r *readbackRepo) Save(_ context.Context, b *Batch) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stored[b.id] = readback(b)
	r.prev[b.id] = readback(b)
	return nil
}

func (r *readbackRepo) Find(_ context.Context, id BatchID) (*Batch, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.stored[id]; s != nil {
		return readback(s), nil
	}
	return nil, nil
}

func (r *readbackRepo) settle(id BatchID, completed, failed int32) (*Batch, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.stored[id]
	if s == nil {
		return nil, false, nil
	}
	r.prev[id] = readback(s)
	done := s.settleSlot(completed, failed, nil)
	return readback(s), done, nil
}

func (r *readbackRepo) IncrementSuccess(_ context.Context, id BatchID) (*Batch, bool, error) {
	return r.settle(id, 1, 0)
}

func (r *readbackRepo) IncrementFailure(_ context.Context, id BatchID, _ error) (*Batch, bool, error) {
	return r.settle(id, 0, 1)
}

func (r *readbackRepo) DecrementPending(_ context.Context, id BatchID) (*Batch, bool, error) {
	return r.settle(id, 0, 0)
}

func (r *readbackRepo) Cancel(_ context.Context, id BatchID) (*Batch, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.stored[id]
	if s == nil {
		return nil, nil
	}
	s.cancelled.Store(true)
	stale := r.prev[id]
	stale.cancelled.Store(true)
	return stale, nil
}

// A readback older than the counters already mirrored onto a batch never
// moves them back: a cancellation whose readback lost the race with a
// failure's mirror keeps that failure counted, and its pending slot taken.
func TestBatch_CancelWithAnOlderReadbackKeepsTheCounters(t *testing.T) {
	resetBatchStoreForTest(t)
	repo := newReadbackRepo()
	defaultBatchRepo.Store(&batchRepoHolder{BatchRepository: repo})
	t.Cleanup(func() { resetBatchStoreForTest(t) })

	b, err := NewBatch(&testBatchJob{}, &testBatchJob{}, &testBatchJob{}).
		Dispatch(context.Background(), newMemoryDriver())
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	// Without AllowFailures the failure cancels the batch; the cancel's
	// readback predates the failure.
	b.recordFailure(context.Background(), errors.New("job broke"))
	if got, pending := b.FailedJobs(), b.PendingJobs(); got != 1 || pending != 2 {
		t.Fatalf("after a failure and its cancel: FailedJobs = %d, PendingJobs = %d; want 1, 2", got, pending)
	}
	if !b.Cancelled() {
		t.Error("batch not cancelled")
	}
}

// The mirror of a repository readback moves every counter forward only,
// whatever order readbacks arrive in: the repository's counters only move
// forward, so the newest readback dominates each field and the merge is
// always one real readback.
func TestBatch_CopyCountersFromNeverMovesBackward(t *testing.T) {
	snap := func(pending, completed, failed int32) *Batch {
		s := &Batch{id: "b", totalJobs: 4}
		s.pendingJobs.Store(pending)
		s.completedJobs.Store(completed)
		s.failedJobs.Store(failed)
		return s
	}
	newer, older := snap(1, 2, 1), snap(3, 1, 0)
	b := snap(4, 0, 0)
	b.copyCountersFrom(newer)
	b.copyCountersFrom(older)
	if p, c, f := b.PendingJobs(), b.CompletedJobs(), b.FailedJobs(); p != 1 || c != 2 || f != 1 {
		t.Fatalf("after newer then older readback: pending %d, completed %d, failed %d; want 1, 2, 1", p, c, f)
	}
}

// Mirrors of readbacks racing one another on the same batch end on the
// newest readback, whatever order they land in. Run under -race.
func TestBatch_CopyCountersFromConcurrentReadbacks(t *testing.T) {
	const n = 64
	for range 50 {
		b := &Batch{id: "b", totalJobs: n}
		b.pendingJobs.Store(n)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := int32(1); i <= n; i++ {
			s := &Batch{id: "b", totalJobs: n}
			s.pendingJobs.Store(n - i)
			s.failedJobs.Store(i)
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				b.copyCountersFrom(s)
			}()
		}
		close(start)
		wg.Wait()
		if p, f := b.PendingJobs(), b.FailedJobs(); p != 0 || f != n {
			t.Fatalf("pending %d, failed %d; want 0, %d", p, f, n)
		}
	}
}

// BenchmarkBatch_CopyCountersFrom measures mirroring a distinct readback
// (the database repository's) onto a batch, once per job outcome.
func BenchmarkBatch_CopyCountersFrom(b *testing.B) {
	src := &Batch{id: "b", totalJobs: 10}
	src.pendingJobs.Store(5)
	src.completedJobs.Store(4)
	src.failedJobs.Store(1)
	b.Run("serial", func(b *testing.B) {
		dst := &Batch{id: "b", totalJobs: 10}
		b.ReportAllocs()
		for b.Loop() {
			dst.copyCountersFrom(src)
		}
	})
	b.Run("parallel", func(b *testing.B) {
		dst := &Batch{id: "b", totalJobs: 10}
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				dst.copyCountersFrom(src)
			}
		})
	})
}
