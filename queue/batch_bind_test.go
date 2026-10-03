package queue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// bindProbeJob is a batch job whose batch methods are the test's: each
// call is counted, then runs the hook when one is set. A nil onQueue hook
// answers the queue field.
type bindProbeJob struct {
	queue string

	mu      sync.Mutex
	batchID BatchID

	onQueue    func()
	setBatchID func(BatchID)
	getBatchID func()

	onQueueCalls    atomic.Int32
	setBatchIDCalls atomic.Int32
	getBatchIDCalls atomic.Int32
	handled         atomic.Int32
}

func (j *bindProbeJob) Handle() error { j.handled.Add(1); return nil }
func (j *bindProbeJob) Failed(error)  {}

func (j *bindProbeJob) OnQueue() string {
	j.onQueueCalls.Add(1)
	if j.onQueue != nil {
		j.onQueue()
	}
	return j.queue
}

func (j *bindProbeJob) SetBatchID(id BatchID) {
	j.setBatchIDCalls.Add(1)
	if j.setBatchID != nil {
		j.setBatchID(id)
	}
	j.mu.Lock()
	j.batchID = id
	j.mu.Unlock()
}

func (j *bindProbeJob) GetBatchID() BatchID {
	j.getBatchIDCalls.Add(1)
	if j.getBatchID != nil {
		j.getBatchID()
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.batchID
}

func registeredCallbackCount() int {
	globalCallbacks.mu.RLock()
	defer globalCallbacks.mu.RUnlock()
	return len(globalCallbacks.entries)
}

var errBindBoom = errors.New("bind boom")

// A job whose SetBatchID or OnQueue panics refuses the batch whole, before
// it exists: Dispatch returns an error naming the job by its position and
// wrapping the recovered panic, nothing is saved, no callbacks are
// registered or fired, no event is dispatched and no job is pushed, the
// ones before the hostile job included.
func TestPendingBatch_Dispatch_PanickingJobMethodRefusesWhole(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		hostile func() *bindProbeJob
	}{
		{"SetBatchID panics", func() *bindProbeJob {
			return &bindProbeJob{setBatchID: func(BatchID) { panic(errBindBoom) }}
		}},
		{"OnQueue panics", func() *bindProbeJob {
			return &bindProbeJob{onQueue: func() { panic(errBindBoom) }}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetBatchStoreForTest(t)
			repo := installCountingRepo(t)
			d := newMemoryDriver()
			first, bad, last := &bindProbeJob{}, tc.hostile(), &bindProbeJob{queue: "q-own"}
			callbacksBefore := registeredCallbackCount()
			var fired, events atomic.Int32

			var b *Batch
			var err error
			hostile.Within(t, hostile.Deadline, func() {
				b, err = NewBatch(first, bad, last).
					OnQueue("q-batch").
					Then(func(*Batch) { fired.Add(1) }).
					Catch(func(*Batch, error) { fired.Add(1) }).
					Finally(func(*Batch) { fired.Add(1) }).
					WithEventDispatcher(func(context.Context, interface{}) { events.Add(1) }).
					Dispatch(ctx, d)
			})

			if err == nil {
				t.Fatal("Dispatch = nil error, want the contained panic")
			}
			if !strings.Contains(err.Error(), "batch: job 2/3") {
				t.Errorf("error %q does not name the job by its position (job 2/3)", err)
			}
			if !errors.Is(err, errBindBoom) {
				t.Errorf("error %q does not wrap the panic value", err)
			}
			if pe := panicerr.AsTyped(err); pe == nil || pe.Recovered() != error(errBindBoom) {
				t.Errorf("error %q carries no *panicerr.Error with the recovered value", err)
			}
			if b != nil {
				t.Errorf("Dispatch returned batch %v for a refused batch", b.ID())
			}
			if n := repo.saves.Load(); n != 0 {
				t.Errorf("refused batch saved %d times, want 0", n)
			}
			if n := registeredCallbackCount(); n != callbacksBefore {
				t.Errorf("refused batch left %d registered callback entries, want %d", n, callbacksBefore)
			}
			for _, q := range []string{"q-batch", "q-own", "default"} {
				if size, _ := d.Size(q); size != 0 {
					t.Errorf("Size(%q) = %d after the refused batch, want 0", q, size)
				}
			}
			if n := last.setBatchIDCalls.Load() + last.onQueueCalls.Load(); n != 0 {
				t.Errorf("the job after the hostile one had %d methods called, want 0", n)
			}
			if _, found := FindBatch(first.GetBatchID()); found {
				t.Errorf("a batch answers to the id the first job was handed (%q)", first.GetBatchID())
			}
			if n := fired.Load(); n != 0 {
				t.Errorf("refused batch fired %d callbacks, want 0", n)
			}
			if n := events.Load(); n != 0 {
				t.Errorf("refused batch dispatched %d events, want 0", n)
			}
		})
	}
}

// The queue of a batch job: its own non-empty OnQueue, else the batch's
// OnQueue, else "default". An empty job name never overrides the batch's.
func TestPendingBatch_Dispatch_QueuePrecedence(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name       string
		job        Job
		batchQueue *string
		want       string
	}{
		{"job name wins over the batch queue", &bindProbeJob{queue: "priority"}, ptr("imports"), "priority"},
		{"empty job name falls back to the batch queue", &bindProbeJob{}, ptr("imports"), "imports"},
		{"no OnQueuer takes the batch queue", &testBatchJob{}, ptr("imports"), "imports"},
		{"empty job name, empty batch queue", &bindProbeJob{}, ptr(""), "default"},
		{"no OnQueuer, empty batch queue", &testBatchJob{}, ptr(""), "default"},
		{"empty job name, batch queue never set", &bindProbeJob{}, nil, "default"},
		{"job name wins with the batch queue never set", &bindProbeJob{queue: "priority"}, nil, "priority"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetBatchStoreForTest(t)
			d := newMemoryDriver()
			pb := NewBatch(tc.job)
			if tc.batchQueue != nil {
				pb.OnQueue(*tc.batchQueue)
			}
			if _, err := pb.Dispatch(ctx, d); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			d.mu.Lock()
			defer d.mu.Unlock()
			for q, jobs := range d.jobs {
				if q != tc.want && len(jobs) != 0 {
					t.Errorf("%d job(s) on %q, want the job on %q only", len(jobs), q, tc.want)
				}
			}
			if n := len(d.jobs[tc.want]); n != 1 {
				t.Errorf("%d job(s) on %q, want 1", n, tc.want)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

// saveProbeRepo runs a hook at Save, before the batch is stored.
type saveProbeRepo struct {
	BatchRepository
	onSave func()
}

func (r *saveProbeRepo) Save(ctx context.Context, b *Batch) error {
	r.onSave()
	return r.BatchRepository.Save(ctx, b)
}

// Every job method Dispatch calls has run by the time the batch is saved,
// each exactly once per job, and none runs between the save and the
// return: the push hands the driver the resolved name.
func TestPendingBatch_Dispatch_JobMethodsRunOnceBeforeTheSave(t *testing.T) {
	resetBatchStoreForTest(t)
	jobs := []*bindProbeJob{{}, {queue: "q-own"}, {}}
	calls := func() (n int32) {
		for _, j := range jobs {
			n += j.onQueueCalls.Load() + j.setBatchIDCalls.Load() + j.getBatchIDCalls.Load()
		}
		return n
	}
	var atSave int32 = -1
	prev := DefaultBatchRepository()
	SetDefaultBatchRepository(&saveProbeRepo{BatchRepository: NewInMemoryBatchRepository(), onSave: func() { atSave = calls() }})
	t.Cleanup(func() { SetDefaultBatchRepository(prev) })

	d := newMemoryDriver()
	if _, err := NewBatch(jobs[0], jobs[1], jobs[2]).OnQueue("q-batch").Dispatch(context.Background(), d); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if want := int32(2 * len(jobs)); atSave != want {
		t.Errorf("%d job method calls before the save, want %d (OnQueue and SetBatchID once per job)", atSave, want)
	}
	if after := calls(); after != atSave {
		t.Errorf("%d job method calls ran after the save, want 0", after-atSave)
	}
	for i, j := range jobs {
		if j.onQueueCalls.Load() != 1 || j.setBatchIDCalls.Load() != 1 {
			t.Errorf("job %d: OnQueue ran %d times, SetBatchID %d, want 1 each", i, j.onQueueCalls.Load(), j.setBatchIDCalls.Load())
		}
	}
	if a, _ := d.Size("q-batch"); a != 2 {
		t.Errorf("Size(q-batch) = %d, want 2", a)
	}
	if a, _ := d.Size("q-own"); a != 1 {
		t.Errorf("Size(q-own) = %d, want 1", a)
	}
}

// A SetBatchID that calls back into the queue package (looks its batch up,
// dispatches a batch of its own) runs under no framework lock: the lookup
// answers not found, since the batch is not saved yet, the inner dispatch
// goes through, and the outer one completes.
func TestPendingBatch_Dispatch_ReenteringSetBatchID(t *testing.T) {
	resetBatchStoreForTest(t)
	ctx := context.Background()
	d := newMemoryDriver()
	var foundEarly atomic.Bool
	var innerErr atomic.Value
	job := &bindProbeJob{}
	job.setBatchID = func(id BatchID) {
		if _, found := FindBatch(id); found {
			foundEarly.Store(true)
		}
		if _, err := NewBatch(&testBatchJob{}).OnQueue("inner").Dispatch(ctx, d); err != nil {
			innerErr.Store(err)
		}
	}
	var b *Batch
	var err error
	hostile.Within(t, hostile.Deadline, func() { b, err = NewBatch(job).OnQueue("outer").Dispatch(ctx, d) })
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if foundEarly.Load() {
		t.Error("the batch was findable while its jobs were still being bound")
	}
	if e := innerErr.Load(); e != nil {
		t.Errorf("inner Dispatch from SetBatchID: %v", e)
	}
	if _, found := FindBatch(b.ID()); !found {
		t.Error("the dispatched batch is not in the repository")
	}
	for _, q := range []string{"inner", "outer"} {
		if size, _ := d.Size(q); size != 1 {
			t.Errorf("Size(%q) = %d, want 1", q, size)
		}
	}
}

// Concurrent dispatches, a third of them holding a job whose SetBatchID
// panics: each good batch is saved and pushed whole, each hostile one is
// refused whole, and none leaves a callback entry behind for a batch that
// does not exist.
func TestPendingBatch_Dispatch_Concurrent(t *testing.T) {
	resetBatchStoreForTest(t)
	repo := installCountingRepo(t)
	ctx := context.Background()
	d := newMemoryDriver()
	const callers, perBatch = 32, 4
	callbacksBefore := registeredCallbackCount()

	var wg sync.WaitGroup
	var good, refused atomic.Int32
	start := make(chan struct{})
	for c := 0; c < callers; c++ {
		wg.Add(1)
		go func(c int) { //safe-goroutine: test fan-out, joined by wg below
			defer wg.Done()
			jobs := make([]Job, perBatch)
			for i := range jobs {
				jobs[i] = &bindProbeJob{}
			}
			if c%3 == 0 {
				jobs[perBatch-1] = &bindProbeJob{setBatchID: func(BatchID) { panic("hostile job") }}
			}
			<-start
			_, err := NewBatch(jobs...).OnQueue("q-stress").Dispatch(ctx, d)
			switch {
			case err == nil:
				good.Add(1)
			case panicerr.AsTyped(err) != nil:
				refused.Add(1)
			default:
				t.Errorf("Dispatch: %v", err)
			}
		}(c)
	}
	close(start)
	hostile.Within(t, hostile.Deadline, wg.Wait)

	wantRefused := int32((callers + 2) / 3)
	if refused.Load() != wantRefused || good.Load() != callers-wantRefused {
		t.Fatalf("good = %d, refused = %d, want %d and %d", good.Load(), refused.Load(), callers-wantRefused, wantRefused)
	}
	if n := repo.saves.Load(); n != good.Load() {
		t.Errorf("%d batches saved, want %d", n, good.Load())
	}
	if size, _ := d.Size("q-stress"); size != int64(good.Load())*perBatch {
		t.Errorf("Size(q-stress) = %d, want %d", size, int64(good.Load())*perBatch)
	}
	if n := registeredCallbackCount() - callbacksBefore; n != int(good.Load()) {
		t.Errorf("%d callback entries registered, want %d (one per saved batch)", n, good.Load())
	}
}

// SetBatchID runs before OnQueue for each job, so an OnQueue that reads
// the job's batch id sees the one the batch handed it.
func TestPendingBatch_Dispatch_OnQueueSeesTheBatchID(t *testing.T) {
	resetBatchStoreForTest(t)
	d := newMemoryDriver()
	job := &bindProbeJob{queue: "q-own"}
	var seen atomic.Value
	job.onQueue = func() {
		job.mu.Lock()
		defer job.mu.Unlock()
		seen.Store(job.batchID)
	}
	b, err := NewBatch(job).Dispatch(context.Background(), d)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got, _ := seen.Load().(BatchID); got != b.ID() {
		t.Errorf("OnQueue saw batch id %q, want %q", got, b.ID())
	}
}

// ctxRepo answers a call made under an ended context with the context's
// error, as a repository over a network does.
type ctxRepo struct{ BatchRepository }

func (r ctxRepo) Find(ctx context.Context, id BatchID) (*Batch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return r.BatchRepository.Find(ctx, id)
}

func (r ctxRepo) Cancel(ctx context.Context, id BatchID) (*Batch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return r.BatchRepository.Cancel(ctx, id)
}

func (r ctxRepo) DecrementPending(ctx context.Context, id BatchID) (*Batch, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	return r.BatchRepository.DecrementPending(ctx, id)
}

// cancellingDriver ends the dispatch context on its nth push and fails
// that push with the context's error; pushes before it are stored.
type cancellingDriver struct {
	*memoryDriver
	cancel context.CancelFunc
	nth    int
	pushes int
}

func (d *cancellingDriver) PushCtx(ctx context.Context, job Job, queue ...string) error {
	d.pushes++
	if d.pushes == d.nth {
		d.cancel()
		return ctx.Err()
	}
	return d.memoryDriver.PushCtx(ctx, job, queue...)
}

// A push that fails because the dispatch context ended still settles the
// batch: the slots of the jobs not pushed are released and the cancel is
// persisted, detached from the ended context. With the first push failing
// every slot is released at once, so the batch finishes and Finally fires.
func TestPendingBatch_Dispatch_PushFailedByCancelledContextStillSettles(t *testing.T) {
	for _, tc := range []struct {
		name         string
		failOn       int
		wantPending  int
		wantFinished bool
	}{
		{"first push", 1, 0, true},
		{"second push", 2, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetBatchStoreForTest(t)
			prev := DefaultBatchRepository()
			SetDefaultBatchRepository(ctxRepo{NewInMemoryBatchRepository()})
			t.Cleanup(func() { SetDefaultBatchRepository(prev) })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			d := &cancellingDriver{memoryDriver: newMemoryDriver(), cancel: cancel, nth: tc.failOn}
			finally := make(chan struct{})

			b, err := NewBatch(&testBatchJob{}, &testBatchJob{}, &testBatchJob{}).
				Finally(func(*Batch) { close(finally) }).
				Dispatch(ctx, d)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Dispatch = %v, want the push's context.Canceled", err)
			}
			if b == nil {
				t.Fatal("Dispatch returned no batch for a saved batch")
			}
			stored, found := FindBatch(b.ID())
			if !found {
				t.Fatal("the saved batch is not in the repository")
			}
			if !stored.Cancelled() {
				t.Error("the cancellation was not persisted")
			}
			if n := stored.PendingJobs(); n != tc.wantPending {
				t.Errorf("PendingJobs = %d, want %d (only the pushed jobs)", n, tc.wantPending)
			}
			if stored.Finished() != tc.wantFinished {
				t.Errorf("Finished = %v, want %v", stored.Finished(), tc.wantFinished)
			}
			if tc.wantFinished {
				select {
				case <-finally:
				case <-time.After(hostile.Deadline):
					t.Error("Finally did not fire for a batch whose every slot was released")
				}
			}
		})
	}
}

// errorLines records the error-level lines written through it and the
// loggers bound from it.
type errorLines struct {
	mu    *sync.Mutex
	lines *[]string
}

func (l errorLines) Debug(string, ...any) {}
func (l errorLines) Info(string, ...any)  {}
func (l errorLines) Warn(string, ...any)  {}
func (l errorLines) Fatal(string, ...any) {}
func (l errorLines) Error(msg string, kvs ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	*l.lines = append(*l.lines, fmt.Sprint(append([]any{msg}, kvs...)...))
}
func (l errorLines) With(...any) contract.Logger { return l }

// failedDriver records the jobs failed through it.
type failedDriver struct {
	*memoryDriver
	fmu    sync.Mutex
	failed []Job
	errs   []error
}

func (d *failedDriver) FailedCtx(_ context.Context, job Job, err error, _ string) error {
	d.fmu.Lock()
	defer d.fmu.Unlock()
	d.failed = append(d.failed, job)
	d.errs = append(d.errs, err)
	return nil
}

// A popped job whose GetBatchID panics must not end the worker: the read
// runs on the pump goroutine, outside the handler's recover. The job is
// not run: it is failed for good with the contained panic, the failure is
// logged once, and the pump goes on to the next job.
func TestWorker_PanickingGetBatchIDIsContained(t *testing.T) {
	resetBatchStoreForTest(t)
	const queueName = "hostile-batch-id"
	d := &failedDriver{memoryDriver: newMemoryDriver()}
	var mu sync.Mutex
	var lines []string
	bad := &bindProbeJob{getBatchID: func() { panic(errBindBoom) }}
	next := &bindProbeJob{}
	w := NewWorker(d, queueName, func(j Job) error { return j.Handle() },
		WithInterval(time.Millisecond), WithWorkerLogger(errorLines{mu: &mu, lines: &lines}))
	if err := d.PushCtx(context.Background(), bad, queueName); err != nil {
		t.Fatalf("push: %v", err)
	}
	if err := d.PushCtx(context.Background(), next, queueName); err != nil {
		t.Fatalf("push next: %v", err)
	}
	w.Start(context.Background())
	t.Cleanup(func() { _ = w.Stop(context.Background()) })

	hostile.Eventually(t, hostile.Deadline, "the worker running the job after the hostile one", func() bool {
		return next.handled.Load() == 1
	})
	hostile.Within(t, hostile.Deadline, func() { _ = w.Stop(context.Background()) })

	if n := bad.handled.Load(); n != 0 {
		t.Errorf("the hostile job ran %d times, want 0", n)
	}
	if n := bad.getBatchIDCalls.Load(); n != 1 {
		t.Errorf("GetBatchID ran %d times for one job, want 1", n)
	}
	if n := next.getBatchIDCalls.Load(); n != 1 {
		t.Errorf("GetBatchID ran %d times for the next job, want 1", n)
	}
	d.fmu.Lock()
	if len(d.failed) != 1 || d.failed[0] != Job(bad) {
		t.Errorf("%d jobs failed, want the hostile job only", len(d.failed))
	} else if err := d.errs[0]; !errors.Is(err, errBindBoom) || panicerr.AsTyped(err) == nil {
		t.Errorf("the job was failed with %v, want the contained panic", err)
	}
	d.fmu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	var hits int
	for _, line := range lines {
		if strings.Contains(line, "GetBatchID panicked") {
			hits++
		}
	}
	if hits != 1 {
		t.Errorf("%d lines report the GetBatchID panic, want 1: %q", hits, lines)
	}
}

// A batch job whose GetBatchID panics on its second call still settles its
// batch: the worker reads the id once, before the handler and the ack, and
// the success accounting reuses that read.
func TestWorker_BatchSettlesOnOneGetBatchIDRead(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fail    bool
		settled func(b *Batch) bool
	}{
		{"job succeeds", false, func(b *Batch) bool { return b.Finished() && b.CompletedJobs() == 1 }},
		{"job fails", true, func(b *Batch) bool { return b.Finished() && b.FailedJobs() == 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetBatchStoreForTest(t)
			const queueName = "second-read"
			d := newMemoryDriver()
			job := &bindProbeJob{}
			job.getBatchID = func() {
				if job.getBatchIDCalls.Load() > 1 {
					panic(errBindBoom)
				}
			}
			finally := make(chan struct{})
			b, err := NewBatch(job).OnQueue(queueName).
				Finally(func(*Batch) { close(finally) }).
				Dispatch(context.Background(), d)
			if err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			w := NewWorker(d, queueName, func(j Job) error {
				if tc.fail {
					return errBindBoom
				}
				return j.Handle()
			}, WithInterval(time.Millisecond), WithMaxRetries(1))
			w.Start(context.Background())
			t.Cleanup(func() { _ = w.Stop(context.Background()) })

			select {
			case <-finally:
			case <-time.After(hostile.Deadline):
				t.Fatal("the batch never settled: Finally did not fire")
			}
			hostile.Within(t, hostile.Deadline, func() { _ = w.Stop(context.Background()) })
			stored, found := FindBatch(b.ID())
			if !found || !tc.settled(stored) {
				t.Errorf("batch not settled: found=%v", found)
			}
			if n := job.getBatchIDCalls.Load(); n != 1 {
				t.Errorf("GetBatchID ran %d times, want 1", n)
			}
		})
	}
}

// batchIDOf answers not batched for a job that is no Batchable, the id for
// one that is, and the contained panic for one whose GetBatchID panics.
func TestBatchIDOf(t *testing.T) {
	if id, batched, err := batchIDOf(&plainBatchJob{}); id != "" || batched || err != nil {
		t.Errorf("not Batchable: %q, %v, %v, want \"\", false, nil", id, batched, err)
	}
	if id, batched, err := batchIDOf(&testBatchJob{batchID: "batch_x"}); id != "batch_x" || !batched || err != nil {
		t.Errorf("Batchable: %q, %v, %v, want batch_x, true, nil", id, batched, err)
	}
	id, batched, err := batchIDOf(&bindProbeJob{getBatchID: func() { panic(errBindBoom) }})
	if id != "" || batched || !errors.Is(err, errBindBoom) || panicerr.AsTyped(err) == nil {
		t.Errorf("panicking GetBatchID: %q, %v, %v, want \"\", false and the contained panic", id, batched, err)
	}
}
