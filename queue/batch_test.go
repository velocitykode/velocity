package queue

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func resetBatchStoreForTest(t *testing.T) {
	t.Helper()
	batchStore.reset()
}

// testBatchJob is a simple job that implements Batchable
type testBatchJob struct {
	batchID BatchID
	handler func() error
	mu      sync.Mutex
}

func (j *testBatchJob) Handle() error {
	if j.handler != nil {
		return j.handler()
	}
	return nil
}

func (j *testBatchJob) Failed(err error) {}

func (j *testBatchJob) GetBatchID() BatchID {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.batchID
}

func (j *testBatchJob) SetBatchID(id BatchID) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.batchID = id
}

// memoryDriver is a simple in-memory queue driver for tests
type memoryDriver struct {
	mu   sync.Mutex
	jobs map[string][]Job
}

func newMemoryDriver() *memoryDriver {
	return &memoryDriver{jobs: make(map[string][]Job)}
}

func (d *memoryDriver) Push(job Job, queue ...string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	q := "default"
	if len(queue) > 0 {
		q = queue[0]
	}
	d.jobs[q] = append(d.jobs[q], job)
	return nil
}

func (d *memoryDriver) PushDelayed(job Job, delay time.Duration, queue ...string) error {
	return d.Push(job, queue...)
}

func (d *memoryDriver) Pop(queue string) (Job, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	jobs := d.jobs[queue]
	if len(jobs) == 0 {
		return nil, nil
	}
	job := jobs[0]
	d.jobs[queue] = jobs[1:]
	return job, nil
}

func (d *memoryDriver) Failed(job Job, err error, queue string) error {
	return nil
}

func (d *memoryDriver) Size(queue string) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return int64(len(d.jobs[queue])), nil
}

func (d *memoryDriver) Clear(queue string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.jobs, queue)
	return nil
}

func (d *memoryDriver) Close() error { return nil }

func TestBatch_SuccessfulCompletion(t *testing.T) {
	resetBatchStoreForTest(t)
	driver := newMemoryDriver()

	var thenCalled atomic.Bool
	var finallyCalled atomic.Bool
	var catchCalled atomic.Bool

	jobs := make([]Job, 3)
	for i := range jobs {
		jobs[i] = &testBatchJob{}
	}

	batch, err := NewBatch(jobs...).
		Then(func(b *Batch) { thenCalled.Store(true) }).
		Catch(func(b *Batch, err error) { catchCalled.Store(true) }).
		Finally(func(b *Batch) { finallyCalled.Store(true) }).
		Dispatch(driver)

	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	if batch.TotalJobs() != 3 {
		t.Errorf("expected 3 total jobs, got %d", batch.TotalJobs())
	}
	if batch.PendingJobs() != 3 {
		t.Errorf("expected 3 pending jobs, got %d", batch.PendingJobs())
	}

	// Simulate all jobs completing
	batch.recordSuccess()
	batch.recordSuccess()
	batch.recordSuccess()

	// Allow callbacks to fire (they run in goroutines)
	time.Sleep(50 * time.Millisecond)

	if !batch.Finished() {
		t.Error("expected batch to be finished")
	}
	if batch.HasFailures() {
		t.Error("expected no failures")
	}
	if !thenCalled.Load() {
		t.Error("expected Then callback to fire")
	}
	if !finallyCalled.Load() {
		t.Error("expected Finally callback to fire")
	}
	if catchCalled.Load() {
		t.Error("expected Catch callback NOT to fire")
	}
	if batch.CompletedJobs() != 3 {
		t.Errorf("expected 3 completed jobs, got %d", batch.CompletedJobs())
	}
	if batch.FailedJobs() != 0 {
		t.Errorf("expected 0 failed jobs, got %d", batch.FailedJobs())
	}
}

func TestBatch_WithFailures_AllowFailures(t *testing.T) {
	resetBatchStoreForTest(t)
	driver := newMemoryDriver()

	var thenCalled atomic.Bool
	var finallyCalled atomic.Bool
	var catchCalled atomic.Bool

	jobs := make([]Job, 3)
	for i := range jobs {
		jobs[i] = &testBatchJob{}
	}

	batch, err := NewBatch(jobs...).
		AllowFailures().
		Then(func(b *Batch) { thenCalled.Store(true) }).
		Catch(func(b *Batch, err error) { catchCalled.Store(true) }).
		Finally(func(b *Batch) { finallyCalled.Store(true) }).
		Dispatch(driver)

	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	// One fails, two succeed
	batch.recordSuccess()
	batch.recordFailure(errors.New("job error"))
	batch.recordSuccess()

	time.Sleep(50 * time.Millisecond)

	if !batch.Finished() {
		t.Error("expected batch to be finished")
	}
	if batch.Cancelled() {
		t.Error("expected batch NOT to be cancelled (AllowFailures is set)")
	}
	if !batch.AllowsFailures() {
		t.Error("expected AllowsFailures() to return true")
	}
	if !batch.HasFailures() {
		t.Error("expected failures")
	}
	if thenCalled.Load() {
		t.Error("expected Then callback NOT to fire (has failures)")
	}
	if !catchCalled.Load() {
		t.Error("expected Catch callback to fire")
	}
	if !finallyCalled.Load() {
		t.Error("expected Finally callback to fire")
	}
	if batch.CompletedJobs() != 2 {
		t.Errorf("expected 2 completed jobs, got %d", batch.CompletedJobs())
	}
	if batch.FailedJobs() != 1 {
		t.Errorf("expected 1 failed job, got %d", batch.FailedJobs())
	}
}

func TestBatch_WithFailures_NoAllowFailures(t *testing.T) {
	resetBatchStoreForTest(t)
	driver := newMemoryDriver()

	jobs := make([]Job, 3)
	for i := range jobs {
		jobs[i] = &testBatchJob{}
	}

	batch, err := NewBatch(jobs...).Dispatch(driver)
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	// First failure should auto-cancel
	batch.recordFailure(errors.New("job error"))

	if !batch.Cancelled() {
		t.Error("expected batch to be cancelled after failure without AllowFailures")
	}
}

func TestBatch_Cancel(t *testing.T) {
	resetBatchStoreForTest(t)
	driver := newMemoryDriver()

	jobs := make([]Job, 3)
	for i := range jobs {
		jobs[i] = &testBatchJob{}
	}

	batch, err := NewBatch(jobs...).Dispatch(driver)
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	batch.Cancel()

	if !batch.Cancelled() {
		t.Error("expected batch to be cancelled")
	}

	// Cancel again should be a no-op (idempotent)
	batch.Cancel()
	if !batch.Cancelled() {
		t.Error("expected batch to still be cancelled")
	}
}

func TestBatch_Progress(t *testing.T) {
	resetBatchStoreForTest(t)
	driver := newMemoryDriver()

	jobs := make([]Job, 4)
	for i := range jobs {
		jobs[i] = &testBatchJob{}
	}

	batch, err := NewBatch(jobs...).AllowFailures().Dispatch(driver)
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	if batch.Progress() != 0.0 {
		t.Errorf("expected 0%% progress, got %.1f%%", batch.Progress())
	}

	batch.recordSuccess()
	if batch.Progress() != 25.0 {
		t.Errorf("expected 25%% progress, got %.1f%%", batch.Progress())
	}

	batch.recordFailure(errors.New("fail"))
	if batch.Progress() != 50.0 {
		t.Errorf("expected 50%% progress, got %.1f%%", batch.Progress())
	}

	batch.recordSuccess()
	if batch.Progress() != 75.0 {
		t.Errorf("expected 75%% progress, got %.1f%%", batch.Progress())
	}

	batch.recordSuccess()
	if batch.Progress() != 100.0 {
		t.Errorf("expected 100%% progress, got %.1f%%", batch.Progress())
	}
}

func TestBatch_FindBatch(t *testing.T) {
	resetBatchStoreForTest(t)
	driver := newMemoryDriver()

	jobs := []Job{&testBatchJob{}}
	batch, err := NewBatch(jobs...).Dispatch(driver)
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	found, ok := FindBatch(batch.ID())
	if !ok {
		t.Fatal("expected to find batch")
	}
	if found.ID() != batch.ID() {
		t.Errorf("expected batch ID %s, got %s", batch.ID(), found.ID())
	}

	// Non-existent batch
	_, ok = FindBatch("nonexistent")
	if ok {
		t.Error("expected not to find nonexistent batch")
	}
}

func TestBatch_EmptyBatch(t *testing.T) {
	resetBatchStoreForTest(t)
	driver := newMemoryDriver()

	_, err := NewBatch().Dispatch(driver)
	if err == nil {
		t.Fatal("expected error for empty batch")
	}
	if err.Error() != "batch: cannot dispatch empty batch" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestBatch_Events(t *testing.T) {
	resetBatchStoreForTest(t)
	driver := newMemoryDriver()

	var events []string
	var eventsMu sync.Mutex
	dispatcher := func(event interface{}) {
		type namer interface{ Name() string }
		if e, ok := event.(namer); ok {
			eventsMu.Lock()
			events = append(events, e.Name())
			eventsMu.Unlock()
		}
	}

	jobs := make([]Job, 2)
	for i := range jobs {
		jobs[i] = &testBatchJob{}
	}

	batch, err := NewBatch(jobs...).
		WithEventDispatcher(dispatcher).
		AllowFailures().
		Dispatch(driver)
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	batch.recordSuccess()
	batch.recordFailure(errors.New("fail"))

	time.Sleep(50 * time.Millisecond)

	eventsMu.Lock()
	defer eventsMu.Unlock()

	expected := map[string]bool{
		"batch.created":       false,
		"batch.job.completed": false,
		"batch.job.failed":    false,
		"batch.completed":     false,
	}

	for _, e := range events {
		expected[e] = true
	}

	for name, found := range expected {
		if !found {
			t.Errorf("expected event %q to be dispatched", name)
		}
	}
}

func TestBatch_CatchFiresOnce(t *testing.T) {
	resetBatchStoreForTest(t)
	driver := newMemoryDriver()

	var catchCount atomic.Int32

	jobs := make([]Job, 3)
	for i := range jobs {
		jobs[i] = &testBatchJob{}
	}

	batch, err := NewBatch(jobs...).
		AllowFailures().
		Catch(func(b *Batch, err error) { catchCount.Add(1) }).
		Dispatch(driver)
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	batch.recordFailure(errors.New("error 1"))
	batch.recordFailure(errors.New("error 2"))
	batch.recordFailure(errors.New("error 3"))

	time.Sleep(50 * time.Millisecond)

	if catchCount.Load() != 1 {
		t.Errorf("expected Catch to fire once, fired %d times", catchCount.Load())
	}
}

func TestBatch_Concurrent(t *testing.T) {
	resetBatchStoreForTest(t)
	driver := newMemoryDriver()

	const numJobs = 100
	jobs := make([]Job, numJobs)
	for i := range jobs {
		jobs[i] = &testBatchJob{}
	}

	var finallyCalled atomic.Bool

	batch, err := NewBatch(jobs...).
		AllowFailures().
		Finally(func(b *Batch) { finallyCalled.Store(true) }).
		Dispatch(driver)
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	// Concurrently record successes and failures
	var wg sync.WaitGroup
	for i := 0; i < numJobs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%3 == 0 {
				batch.recordFailure(errors.New("fail"))
			} else {
				batch.recordSuccess()
			}
		}(i)
	}
	wg.Wait()

	time.Sleep(50 * time.Millisecond)

	if !batch.Finished() {
		t.Error("expected batch to be finished")
	}
	if !finallyCalled.Load() {
		t.Error("expected Finally to fire")
	}

	total := batch.CompletedJobs() + batch.FailedJobs()
	if total != numJobs {
		t.Errorf("expected %d processed jobs, got %d", numJobs, total)
	}
	if batch.PendingJobs() != 0 {
		t.Errorf("expected 0 pending jobs, got %d", batch.PendingJobs())
	}
}

func TestBatch_BatchableJobsGetBatchID(t *testing.T) {
	resetBatchStoreForTest(t)
	driver := newMemoryDriver()

	job1 := &testBatchJob{}
	job2 := &testBatchJob{}

	batch, err := NewBatch(job1, job2).Dispatch(driver)
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	if job1.GetBatchID() != batch.ID() {
		t.Errorf("expected job1 BatchID %s, got %s", batch.ID(), job1.GetBatchID())
	}
	if job2.GetBatchID() != batch.ID() {
		t.Errorf("expected job2 BatchID %s, got %s", batch.ID(), job2.GetBatchID())
	}
}

func TestBatch_OnQueue(t *testing.T) {
	resetBatchStoreForTest(t)
	driver := newMemoryDriver()

	jobs := []Job{&testBatchJob{}}
	batch, err := NewBatch(jobs...).OnQueue("high").Dispatch(driver)
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	// Verify jobs were pushed to the correct queue
	size, _ := driver.Size("high")
	if size != 1 {
		t.Errorf("expected 1 job on 'high' queue, got %d", size)
	}

	// Verify batch tracks the queue
	if batch.queue != "high" {
		t.Errorf("expected batch queue 'high', got '%s'", batch.queue)
	}
}

func TestBatch_WorkerIntegration(t *testing.T) {
	resetBatchStoreForTest(t)
	driver := newMemoryDriver()

	var finallyCalled atomic.Bool
	var thenCalled atomic.Bool

	job1 := &testBatchJob{handler: func() error { return nil }}
	job2 := &testBatchJob{handler: func() error { return nil }}

	batch, err := NewBatch(job1, job2).
		Then(func(b *Batch) { thenCalled.Store(true) }).
		Finally(func(b *Batch) { finallyCalled.Store(true) }).
		Dispatch(driver)
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	worker := NewWorker(driver, "default", func(job Job) error {
		return job.Handle()
	}, WithInterval(10*time.Millisecond), WithMaxRetries(0))

	worker.Start()

	// Wait for jobs to be processed
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			worker.Stop()
			t.Fatal("timed out waiting for batch to finish")
		default:
			if batch.Finished() {
				worker.Stop()
				time.Sleep(50 * time.Millisecond)
				if !thenCalled.Load() {
					t.Error("expected Then callback to fire")
				}
				if !finallyCalled.Load() {
					t.Error("expected Finally callback to fire")
				}
				if batch.CompletedJobs() != 2 {
					t.Errorf("expected 2 completed, got %d", batch.CompletedJobs())
				}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestBatch_CancelledJobSkipped(t *testing.T) {
	resetBatchStoreForTest(t)
	driver := newMemoryDriver()

	var handled atomic.Bool
	var finallyCalled atomic.Bool
	job1 := &testBatchJob{handler: func() error {
		handled.Store(true)
		return nil
	}}

	batch, err := NewBatch(job1).
		Finally(func(b *Batch) { finallyCalled.Store(true) }).
		Dispatch(driver)
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	// Cancel before worker processes
	batch.Cancel()

	worker := NewWorker(driver, "default", func(job Job) error {
		return job.Handle()
	}, WithInterval(10*time.Millisecond))

	worker.Start()
	time.Sleep(200 * time.Millisecond)
	worker.Stop()

	if handled.Load() {
		t.Error("expected cancelled batch job to be skipped")
	}
	// Skipped jobs should decrement pendingJobs so batch reaches Finished
	if !batch.Finished() {
		t.Error("expected cancelled batch to reach Finished state")
	}
	if batch.PendingJobs() != 0 {
		t.Errorf("expected 0 pending jobs after skip, got %d", batch.PendingJobs())
	}
	// Finally should still fire even when all jobs are skipped
	time.Sleep(50 * time.Millisecond)
	if !finallyCalled.Load() {
		t.Error("expected Finally callback to fire for cancelled batch")
	}
}

func TestBatch_ProgressEmptyBatch(t *testing.T) {
	b := &Batch{totalJobs: 0}
	if b.Progress() != 100.0 {
		t.Errorf("expected 100%% for empty batch, got %.1f%%", b.Progress())
	}
}

func TestBatch_AllowsFailures_DefaultFalse(t *testing.T) {
	resetBatchStoreForTest(t)
	driver := newMemoryDriver()

	jobs := []Job{&testBatchJob{}}
	batch, err := NewBatch(jobs...).Dispatch(driver)
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	if batch.AllowsFailures() {
		t.Error("expected AllowsFailures() to be false by default")
	}
}

func TestBatch_DispatchPushFailure(t *testing.T) {
	resetBatchStoreForTest(t)
	failDriver := &failingDriver{failOnNth: 2}

	job1 := &testBatchJob{}
	job2 := &testBatchJob{}
	job3 := &testBatchJob{}

	batch, err := NewBatch(job1, job2, job3).Dispatch(failDriver)
	if err == nil {
		t.Fatal("expected error when driver.Push fails")
	}
	if batch == nil {
		t.Fatal("expected batch to be returned even on partial failure")
	}
	// Driver was called twice: 1 success + 1 failure
	if failDriver.pushed != 2 {
		t.Errorf("expected 2 push attempts (1 success + 1 failure), got %d", failDriver.pushed)
	}
	// Batch should be cancelled on partial push failure
	if !batch.Cancelled() {
		t.Error("expected batch to be cancelled after partial push failure")
	}
	// pendingJobs should be adjusted: only 1 job was actually pushed
	if batch.PendingJobs() != 1 {
		t.Errorf("expected 1 pending job (only 1 pushed), got %d", batch.PendingJobs())
	}
}

func TestBatch_CancelledEvent(t *testing.T) {
	resetBatchStoreForTest(t)
	driver := newMemoryDriver()

	var events []string
	var eventsMu sync.Mutex
	dispatcher := func(event interface{}) {
		type namer interface{ Name() string }
		if e, ok := event.(namer); ok {
			eventsMu.Lock()
			events = append(events, e.Name())
			eventsMu.Unlock()
		}
	}

	jobs := []Job{&testBatchJob{}}
	batch, err := NewBatch(jobs...).
		WithEventDispatcher(dispatcher).
		Dispatch(driver)
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	batch.Cancel()

	eventsMu.Lock()
	defer eventsMu.Unlock()

	found := false
	for _, e := range events {
		if e == "batch.cancelled" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected batch.cancelled event, got %v", events)
	}
}

// failingDriver fails Push on the Nth call
type failingDriver struct {
	pushed    int
	failOnNth int
	mu        sync.Mutex
}

func (d *failingDriver) Push(job Job, queue ...string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pushed++
	if d.pushed >= d.failOnNth {
		return errors.New("push failed")
	}
	return nil
}

func (d *failingDriver) PushDelayed(Job, time.Duration, ...string) error { return nil }
func (d *failingDriver) Pop(string) (Job, error)                        { return nil, nil }
func (d *failingDriver) Size(string) (int64, error)                     { return 0, nil }
func (d *failingDriver) Clear(string) error                             { return nil }
func (d *failingDriver) Failed(Job, error, string) error                { return nil }
func (d *failingDriver) Close() error                                   { return nil }

// testOnQueuerJob implements both Batchable and OnQueuer
type testOnQueuerJob struct {
	testBatchJob
	queue string
}

func (j *testOnQueuerJob) OnQueue() string { return j.queue }

func TestBatch_JobOnQueuerOverridesBatchQueue(t *testing.T) {
	resetBatchStoreForTest(t)
	driver := newMemoryDriver()

	regularJob := &testBatchJob{}
	priorityJob := &testOnQueuerJob{queue: "priority"}

	_, err := NewBatch(regularJob, priorityJob).OnQueue("default").Dispatch(driver)
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	defaultSize, _ := driver.Size("default")
	prioritySize, _ := driver.Size("priority")

	if defaultSize != 1 {
		t.Errorf("expected 1 job on 'default' queue, got %d", defaultSize)
	}
	if prioritySize != 1 {
		t.Errorf("expected 1 job on 'priority' queue, got %d", prioritySize)
	}
}

func TestBatchStore_Close(t *testing.T) {
	s := newBatchStore()
	s.close()
	// Second close should not panic (idempotent)
	s.close()
}
