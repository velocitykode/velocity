package queue

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// waitFor polls the condition function until it returns true or the timeout expires.
func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		if condition() {
			return
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for condition")
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func TestWorker(t *testing.T) {
	q := NewMemoryDriver()
	q.Start()

	t.Run("Basic Worker", func(t *testing.T) {
		processed := int32(0)

		worker := NewWorker(q, "worker-test", func(job Job) error {
			atomic.AddInt32(&processed, 1)
			return nil
		})

		for i := 0; i < 5; i++ {
			job := &TestJob{
				ID:      "worker-" + string(rune(i)),
				Message: "Worker test",
			}
			err := q.PushCtx(context.Background(), job, "worker-test")
			if err != nil {
				t.Fatalf("Failed to push job: %v", err)
			}
		}

		worker.Start(context.Background())
		waitFor(t, 5*time.Second, func() bool {
			return atomic.LoadInt32(&processed) == 5
		})
		worker.Stop()

		if atomic.LoadInt32(&processed) != 5 {
			t.Errorf("Expected 5 jobs processed, got %d", processed)
		}
	})

	t.Run("Concurrent Workers", func(t *testing.T) {
		processed := int32(0)

		worker := NewWorker(q, "concurrent-worker", func(job Job) error {
			atomic.AddInt32(&processed, 1)
			time.Sleep(10 * time.Millisecond) // Simulate work
			return nil
		}, WithConcurrency(3))

		for i := 0; i < 10; i++ {
			job := &TestJob{
				ID:      "concurrent-" + string(rune(i)),
				Message: "Concurrent worker test",
			}
			err := q.PushCtx(context.Background(), job, "concurrent-worker")
			if err != nil {
				t.Fatalf("Failed to push job: %v", err)
			}
		}

		worker.Start(context.Background())
		waitFor(t, 5*time.Second, func() bool {
			return atomic.LoadInt32(&processed) == 10
		})
		worker.Stop()

		if atomic.LoadInt32(&processed) != 10 {
			t.Errorf("Expected 10 jobs processed, got %d", processed)
		}
	})

	t.Run("Failed Jobs", func(t *testing.T) {
		processed := int32(0)
		failed := int32(0)

		worker := NewWorker(q, "fail-worker", func(job Job) error {
			count := atomic.AddInt32(&processed, 1)
			if count%2 == 0 {
				return errors.New("simulated failure")
			}
			return nil
		}, WithMaxRetries(1))

		for i := 0; i < 6; i++ {
			job := &TestJob{
				ID:      "fail-" + string(rune(i)),
				Message: "Fail worker test",
				OnFail: func(err error) {
					atomic.AddInt32(&failed, 1)
				},
			}
			err := q.PushCtx(context.Background(), job, "fail-worker")
			if err != nil {
				t.Fatalf("Failed to push job: %v", err)
			}
		}

		worker.Start(context.Background())
		waitFor(t, 5*time.Second, func() bool {
			return atomic.LoadInt32(&processed) >= 6
		})
		worker.Stop()

		if atomic.LoadInt32(&processed) != 6 {
			t.Errorf("Expected 6 jobs attempted, got %d", processed)
		}

		if atomic.LoadInt32(&failed) != 3 {
			t.Errorf("Expected 3 jobs failed, got %d", failed)
		}
	})

	t.Run("Worker Timeout", func(t *testing.T) {
		timedOut := int32(0)

		job := &TestJob{
			ID:      "timeout-1",
			Message: "Timeout test",
			Handler: func() error {
				time.Sleep(5 * time.Second) // Longer than 1s timeout
				return nil
			},
			OnFail: func(err error) {
				if err.Error() == "velocity/queue: job timed out" {
					atomic.AddInt32(&timedOut, 1)
				}
			},
		}

		err := q.PushCtx(context.Background(), job, "timeout-worker")
		if err != nil {
			t.Fatalf("Failed to push job: %v", err)
		}

		worker := NewWorker(q, "timeout-worker", func(j Job) error {
			return j.Handle()
		}, WithInterval(50*time.Millisecond), WithMaxRetries(1), WithTimeout(1*time.Second))

		worker.Start(context.Background())
		waitFor(t, 10*time.Second, func() bool {
			return atomic.LoadInt32(&timedOut) == 1
		})
		worker.Stop()

		if atomic.LoadInt32(&timedOut) != 1 {
			t.Errorf("Expected job to timeout, but it didn't")
		}
	})
}

func TestGlobalWorker(t *testing.T) {
	q := NewMemoryDriver()
	q.Start()

	processed := int32(0)

	for i := 0; i < 3; i++ {
		job := &TestJob{
			ID:      "global-worker-" + string(rune(i)),
			Message: "Global worker test",
		}
		err := q.PushCtx(context.Background(), job, "global-worker")
		if err != nil {
			t.Fatalf("Failed to push job: %v", err)
		}
	}

	worker := NewWorker(q, "global-worker", func(job Job) error {
		atomic.AddInt32(&processed, 1)
		return nil
	}, WithConcurrency(2))
	worker.Start(context.Background())

	waitFor(t, 5*time.Second, func() bool {
		return atomic.LoadInt32(&processed) == 3
	})
	worker.Stop()

	if atomic.LoadInt32(&processed) != 3 {
		t.Errorf("Expected 3 jobs processed, got %d", processed)
	}
}

func TestWorker_RetryOnFailure(t *testing.T) {
	q := NewMemoryDriver()
	q.Start()
	defer q.Shutdown(context.Background())

	attempts := int32(0)

	job := &TestJob{
		ID:      "retry-success",
		Message: "fails twice then succeeds",
		Handler: func() error {
			n := atomic.AddInt32(&attempts, 1)
			if n <= 2 {
				return errors.New("transient error")
			}
			return nil
		},
	}

	err := q.PushCtx(context.Background(), job, "retry-queue")
	if err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	worker := NewWorker(q, "retry-queue", func(j Job) error {
		return j.Handle()
	},
		WithInterval(50*time.Millisecond),
		WithMaxRetries(5),
		WithBackoff(FixedBackoff(0)),
	)

	worker.Start(context.Background())
	waitFor(t, 10*time.Second, func() bool {
		return atomic.LoadInt32(&attempts) >= 3
	})
	worker.Stop()

	got := atomic.LoadInt32(&attempts)
	if got != 3 {
		t.Errorf("Expected 3 attempts (2 failures + 1 success), got %d", got)
	}
}

func TestWorker_ExhaustsRetries(t *testing.T) {
	q := NewMemoryDriver()
	q.Start()
	defer q.Shutdown(context.Background())

	attempts := int32(0)
	failed := int32(0)

	job := &TestJob{
		ID:      "always-fail",
		Message: "always fails",
		Handler: func() error {
			atomic.AddInt32(&attempts, 1)
			return errors.New("permanent error")
		},
		OnFail: func(err error) {
			atomic.AddInt32(&failed, 1)
		},
	}

	err := q.PushCtx(context.Background(), job, "exhaust-queue")
	if err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	worker := NewWorker(q, "exhaust-queue", func(j Job) error {
		return j.Handle()
	},
		WithInterval(50*time.Millisecond),
		WithMaxRetries(3),
		WithBackoff(FixedBackoff(0)),
	)

	worker.Start(context.Background())
	waitFor(t, 10*time.Second, func() bool {
		return atomic.LoadInt32(&failed) >= 1
	})
	worker.Stop()

	gotAttempts := atomic.LoadInt32(&attempts)
	if gotAttempts != 3 {
		t.Errorf("Expected 3 total attempts, got %d", gotAttempts)
	}

	gotFailed := atomic.LoadInt32(&failed)
	if gotFailed != 1 {
		t.Errorf("Expected Failed() called once, got %d", gotFailed)
	}
}

func TestWorker_NoRetryWhenMaxRetriesIsOne(t *testing.T) {
	q := NewMemoryDriver()
	q.Start()
	defer q.Shutdown(context.Background())

	attempts := int32(0)
	failed := int32(0)

	job := &TestJob{
		ID:      "no-retry",
		Message: "no retries",
		Handler: func() error {
			atomic.AddInt32(&attempts, 1)
			return errors.New("fail")
		},
		OnFail: func(err error) {
			atomic.AddInt32(&failed, 1)
		},
	}

	err := q.PushCtx(context.Background(), job, "no-retry-queue")
	if err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	worker := NewWorker(q, "no-retry-queue", func(j Job) error {
		return j.Handle()
	},
		WithInterval(50*time.Millisecond),
		WithMaxRetries(1),
	)

	worker.Start(context.Background())
	waitFor(t, 5*time.Second, func() bool {
		return atomic.LoadInt32(&failed) >= 1
	})
	worker.Stop()

	gotAttempts := atomic.LoadInt32(&attempts)
	if gotAttempts != 1 {
		t.Errorf("Expected exactly 1 attempt with maxRetries=1, got %d", gotAttempts)
	}

	gotFailed := atomic.LoadInt32(&failed)
	if gotFailed != 1 {
		t.Errorf("Expected Failed() called once, got %d", gotFailed)
	}
}

// retryDeciderJob wraps TestJob and implements RetryDecider
type retryDeciderJob struct {
	TestJob
	shouldRetry bool
}

func (r *retryDeciderJob) ShouldRetry(_ error) bool {
	return r.shouldRetry
}

func TestWorker_RetryDeciderStopsRetry(t *testing.T) {
	q := NewMemoryDriver()
	q.Start()
	defer q.Shutdown(context.Background())

	attempts := int32(0)
	failed := int32(0)

	job := &retryDeciderJob{
		TestJob: TestJob{
			ID:      "decider-stop",
			Message: "stops retry",
			Handler: func() error {
				atomic.AddInt32(&attempts, 1)
				return errors.New("non-retryable")
			},
			OnFail: func(err error) {
				atomic.AddInt32(&failed, 1)
			},
		},
		shouldRetry: false,
	}

	err := q.PushCtx(context.Background(), job, "decider-queue")
	if err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	worker := NewWorker(q, "decider-queue", func(j Job) error {
		return j.Handle()
	},
		WithInterval(50*time.Millisecond),
		WithMaxRetries(5),
		WithBackoff(FixedBackoff(0)),
	)

	worker.Start(context.Background())
	waitFor(t, 5*time.Second, func() bool {
		return atomic.LoadInt32(&failed) >= 1
	})
	worker.Stop()

	gotAttempts := atomic.LoadInt32(&attempts)
	if gotAttempts != 1 {
		t.Errorf("Expected 1 attempt (RetryDecider stopped retry), got %d", gotAttempts)
	}

	gotFailed := atomic.LoadInt32(&failed)
	if gotFailed != 1 {
		t.Errorf("Expected Failed() called once, got %d", gotFailed)
	}
}

// maxAttempterJob wraps TestJob and implements MaxAttempter
type maxAttempterJob struct {
	TestJob
	maxAttempts int
}

func (m *maxAttempterJob) MaxAttempts() int {
	return m.maxAttempts
}

func TestWorker_MaxAttempterInterface(t *testing.T) {
	q := NewMemoryDriver()
	q.Start()
	defer q.Shutdown(context.Background())

	attempts := int32(0)
	failed := int32(0)

	job := &maxAttempterJob{
		TestJob: TestJob{
			ID:      "max-attempter",
			Message: "custom max",
			Handler: func() error {
				atomic.AddInt32(&attempts, 1)
				return errors.New("fail")
			},
			OnFail: func(err error) {
				atomic.AddInt32(&failed, 1)
			},
		},
		maxAttempts: 2, // Override worker default of 3
	}

	err := q.PushCtx(context.Background(), job, "max-attempter-queue")
	if err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	worker := NewWorker(q, "max-attempter-queue", func(j Job) error {
		return j.Handle()
	},
		WithInterval(50*time.Millisecond),
		WithMaxRetries(10), // Worker default is high, but job says 2
		WithBackoff(FixedBackoff(0)),
	)

	worker.Start(context.Background())
	waitFor(t, 10*time.Second, func() bool {
		return atomic.LoadInt32(&failed) >= 1
	})
	worker.Stop()

	gotAttempts := atomic.LoadInt32(&attempts)
	if gotAttempts != 2 {
		t.Errorf("Expected 2 attempts (MaxAttempter override), got %d", gotAttempts)
	}

	gotFailed := atomic.LoadInt32(&failed)
	if gotFailed != 1 {
		t.Errorf("Expected Failed() called once, got %d", gotFailed)
	}
}

// TestWorker_ConcurrencyCap verifies that WithConcurrency clamps values that
// exceed MaxWorkerConcurrency so a mis-typed configuration can't spawn an
// unreasonable number of goroutines.
func TestWorker_ConcurrencyCap(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"zero leaves default", 0, 1},
		{"negative leaves default", -5, 1},
		{"one ok", 1, 1},
		{"exactly at cap ok", MaxWorkerConcurrency, MaxWorkerConcurrency},
		{"above cap clamps", MaxWorkerConcurrency + 1, MaxWorkerConcurrency},
		{"very large clamps", 1_000_000, MaxWorkerConcurrency},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := NewMemoryDriver()
			q.Start()
			defer q.Shutdown(context.Background())
			w := NewWorker(q, "cap", func(Job) error { return nil }, WithConcurrency(tc.in))
			if w.concurrency != tc.want {
				t.Errorf("concurrency = %d, want %d", w.concurrency, tc.want)
			}
		})
	}
}

// backofferJob wraps TestJob and implements Backoffer
type backofferJob struct {
	TestJob
	delays []time.Duration
}

func (b *backofferJob) Backoff() []time.Duration {
	return b.delays
}

func TestWorker_BackofferInterface(t *testing.T) {
	q := NewMemoryDriver()
	q.Start()
	defer q.Shutdown(context.Background())

	attempts := int32(0)

	job := &backofferJob{
		TestJob: TestJob{
			ID:      "backoffer",
			Message: "custom backoff",
			Handler: func() error {
				n := atomic.AddInt32(&attempts, 1)
				if n <= 2 {
					return errors.New("fail")
				}
				return nil
			},
		},
		delays: []time.Duration{0, 0}, // Zero delays for fast test
	}

	err := q.PushCtx(context.Background(), job, "backoffer-queue")
	if err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	worker := NewWorker(q, "backoffer-queue", func(j Job) error {
		return j.Handle()
	},
		WithInterval(50*time.Millisecond),
		WithMaxRetries(5),
	)

	worker.Start(context.Background())
	waitFor(t, 10*time.Second, func() bool {
		return atomic.LoadInt32(&attempts) >= 3
	})
	worker.Stop()

	gotAttempts := atomic.LoadInt32(&attempts)
	if gotAttempts != 3 {
		t.Errorf("Expected 3 attempts (2 failures with Backoffer delays + 1 success), got %d", gotAttempts)
	}
}

// TestWorker_CtxCancelPropagatesToJobExecution verifies that cancelling the
// context passed to Worker.Start propagates down into the per-job context
// observed by the job-execution select in processJob. This guards against
// the regression where the worker previously owned a context derived from
// context.Background() and ignored the parent context entirely, so
// application-level shutdown deadlines could not abort an in-flight job.
//
// The handler blocks on time.Sleep. Before the fix, cancelling the parent
// ctx had no effect: the worker's internal ctx was rooted at Background(),
// so Stop() was the only way to abort. After the fix, cancelling parent
// ctx cancels worker ctx, which cancels jobCtx, which makes processJob's
// select observe <-jobCtx.Done() and return, letting Stop() complete.
func TestWorker_CtxCancelPropagatesToJobExecution(t *testing.T) {
	// Shrink the handler kill ceiling for this test: the handler ignores
	// ctx and sleeps 60s, so drainHandler in processJob would otherwise
	// wait the production-default 5s before letting the pump return.
	// Restored on cleanup so other tests run at the production value.
	prev := defaultHandlerKillCeiling
	defaultHandlerKillCeiling = 200 * time.Millisecond
	t.Cleanup(func() { defaultHandlerKillCeiling = prev })

	q := NewMemoryDriver()
	q.Start()
	defer q.Shutdown(context.Background())

	handlerStarted := make(chan struct{})
	var once sync.Once

	// Handler blocks far longer than any test deadline; it will only unblock
	// when abandoned by the worker on ctx cancel (processJob's select returns
	// via <-jobCtx.Done() and the handler's goroutine leaks briefly).
	job := &TestJob{
		ID:      "ctx-propagation",
		Message: "blocks until abandoned",
		Handler: func() error {
			once.Do(func() { close(handlerStarted) })
			time.Sleep(60 * time.Second)
			return nil
		},
	}

	if err := q.PushCtx(context.Background(), job, "ctx-prop-queue"); err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	parentCtx, parentCancel := context.WithCancel(context.Background())
	defer parentCancel()

	worker := NewWorker(q, "ctx-prop-queue", func(j Job) error {
		return j.Handle()
	}, WithInterval(10*time.Millisecond), WithMaxRetries(1), WithTimeout(60*time.Second))

	worker.Start(parentCtx)

	// Wait for the handler to actually start processing the job.
	select {
	case <-handlerStarted:
	case <-time.After(2 * time.Second):
		worker.Stop()
		t.Fatal("handler never started; worker did not pick up job")
	}

	// Cancel the parent context. If ctx propagation is wired correctly,
	// this alone cancels worker.ctx → jobCtx, which makes processJob's
	// select observe <-jobCtx.Done() and return. The pump goroutines then
	// observe <-w.ctx.Done() on their next loop iteration and exit.
	//
	// We intentionally do NOT call worker.Stop() to force exit; the test
	// relies purely on parent ctx propagation to drive shutdown. Waiting
	// on the worker's internal WaitGroup directly verifies that the pumps
	// exited on their own.
	parentCancel()

	done := make(chan struct{})
	go func() {
		worker.wg.Wait()
		close(done)
	}()

	// Bound: kill ceiling (drainHandler) + pump-loop slack. If propagation
	// is wired correctly the pumps exit promptly once jobCtx fires and
	// drainHandler returns.
	maxWait := defaultHandlerKillCeiling + 3*time.Second
	select {
	case <-done:
	case <-time.After(maxWait):
		// Last-ditch cleanup to avoid leaked goroutines confusing later tests.
		worker.Stop()
		t.Fatalf("worker pumps did not exit after parent ctx cancel within %v "+
			"(ctx propagation bug: worker ctx is not derived from parent)", maxWait)
	}
}

// slowPushDriver wraps MemoryDriver so PushDelayedCtx blocks until the
// driver's unblock channel is closed (or the test's timeout fires). This
// models a Redis partition or DB lock wait during a retry-requeue.
type slowPushDriver struct {
	*MemoryDriver
	unblock     chan struct{}
	pushEntered chan struct{}
	once        sync.Once
	enteredOnce sync.Once
}

func newSlowPushDriver() *slowPushDriver {
	return &slowPushDriver{
		MemoryDriver: NewMemoryDriver(),
		unblock:      make(chan struct{}),
		pushEntered:  make(chan struct{}),
	}
}

// PopCtxReserved opts this test wrapper out of the ReservationDriver
// path the underlying *MemoryDriver provides, so the worker exercises
// the legacy PushDelayedCtx retry route this test was written to cover.
// The embed promotes PopCtxReserved on the memory driver; without this
// shadow the worker would call ReleaseCtx on retry and never touch
// PushDelayedCtx, defeating the test's purpose.
func (d *slowPushDriver) PopCtxReserved(ctx context.Context, queue string) (Job, ReservationToken, TraceContext, error) {
	job, tc, err := d.MemoryDriver.PopCtxWithTrace(ctx, queue)
	return job, ReservationToken{}, tc, err
}

func (d *slowPushDriver) PushDelayedCtx(ctx context.Context, job Job, delay time.Duration, queue ...string) error {
	d.enteredOnce.Do(func() { close(d.pushEntered) })
	// Model a Redis network partition or DB lock wait: the driver's
	// underlying network call is unresponsive to ctx cancellation until
	// some external unblock happens. This forces the worker to use a
	// separate bounded context so it can move on even when the driver is
	// genuinely stuck. We deliberately do NOT select on <-ctx.Done() here —
	// if the worker passed its own w.ctx and relied on Stop() cancelling
	// it, this push would still hang. The fix under test supplies a
	// short-timeout detached ctx that fires its own Done() regardless.
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		// The worker is not using a bounded ctx — this is the buggy case.
		// Block forever until explicitly unblocked by the test.
		<-d.unblock
		return d.MemoryDriver.PushDelayedCtx(ctx, job, delay, queue...)
	}
	// Bounded ctx — wait for its deadline (simulates driver still hanging
	// on its network call until the worker's timeout fires).
	select {
	case <-d.unblock:
		return d.MemoryDriver.PushDelayedCtx(ctx, job, delay, queue...)
	case <-time.After(time.Until(deadline) + 50*time.Millisecond):
		return context.DeadlineExceeded
	}
}

func (d *slowPushDriver) release() {
	d.once.Do(func() { close(d.unblock) })
}

// TestWorker_RetryPushBoundedDuringShutdown verifies that a slow
// PushDelayedCtx (simulating a Redis partition or DB lock) does not hold
// the worker's Stop() call open past the retry-push timeout. The retry
// push runs on a detached context with a short timeout; shutdown must
// complete within that budget plus slack.
func TestWorker_RetryPushBoundedDuringShutdown(t *testing.T) {
	driver := newSlowPushDriver()
	driver.Start()
	defer driver.Shutdown(context.Background())
	defer driver.release() // ensure test cleanup even on failure

	// Push a job that will fail, triggering the retry path that pushes back.
	job := &TestJob{
		ID:      "retry-bounded",
		Message: "fail once to trigger retry push",
		Handler: func() error {
			return errors.New("force retry")
		},
	}
	if err := driver.PushCtx(context.Background(), job, "retry-bound-queue"); err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	worker := NewWorker(driver, "retry-bound-queue", func(j Job) error {
		return j.Handle()
	},
		WithInterval(10*time.Millisecond),
		WithMaxRetries(5), // ensure retry branch is taken, not failJob
		WithBackoff(FixedBackoff(0)),
	)

	worker.Start(context.Background())

	// Wait until the retry push is in-flight (blocked on the slow driver).
	select {
	case <-driver.pushEntered:
	case <-time.After(5 * time.Second):
		worker.Stop()
		t.Fatal("PushDelayedCtx never entered; worker did not reach retry path")
	}

	// Shutdown must complete within retryPushTimeout + slack, not hang forever.
	done := make(chan struct{})
	start := time.Now()
	go func() {
		worker.Stop()
		close(done)
	}()

	maxWait := retryPushTimeout + 3*time.Second
	select {
	case <-done:
		elapsed := time.Since(start)
		if elapsed > maxWait {
			t.Errorf("Stop took %v, expected <= %v", elapsed, maxWait)
		}
	case <-time.After(maxWait):
		driver.release() // unblock the driver so the goroutine can exit
		<-done
		t.Fatalf("Stop did not complete within %v (retry push hung)", maxWait)
	}
}

// ctxAwareJob implements HandleCtxer and blocks on ctx.Done(), recording the
// observed ctx.Err() so the test can assert cancellation propagated into the
// handler.
type ctxAwareJob struct {
	ID string `json:"id"`
	// In-process test-only fields. Excluded from JSON: the memory driver
	// keeps the live pointer on JobWrapper.Job for same-process pops, so
	// these channels / atomic values survive without going through the
	// payload bytes (which now carry only ID for cross-process workers).
	started    chan struct{} `json:"-"`
	observed   atomic.Value  `json:"-"` // error
	failedWith atomic.Value  `json:"-"` // error
}

func (j *ctxAwareJob) Handle() error {
	// Should never be called when HandleCtxer is implemented.
	return errors.New("ctxAwareJob.Handle called instead of HandleCtx")
}

func (j *ctxAwareJob) HandleCtx(ctx context.Context) error {
	close(j.started)
	<-ctx.Done()
	j.observed.Store(ctx.Err())
	return ctx.Err()
}

func (j *ctxAwareJob) Failed(err error) {
	j.failedWith.Store(err)
}

// JobID gives a stable key so the worker's attempt tracker doesn't fall back
// to pointer identity (irrelevant here but mirrors real-world usage).
func (j *ctxAwareJob) JobID() string { return j.ID }

func TestWorker_HandleCtxerReceivesCancellation(t *testing.T) {
	q := NewMemoryDriver()
	q.Start()
	defer q.Shutdown(context.Background())

	job := &ctxAwareJob{
		ID:      "ctx-aware-1",
		started: make(chan struct{}),
	}

	if err := q.PushCtx(context.Background(), job, "ctx-queue"); err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	// User-supplied handler should NOT be invoked when the job implements
	// HandleCtxer; track that here as a regression guard.
	handlerCalls := int32(0)
	worker := NewWorker(q, "ctx-queue", func(j Job) error {
		atomic.AddInt32(&handlerCalls, 1)
		return j.Handle()
	},
		WithInterval(25*time.Millisecond),
		WithMaxRetries(1),
		WithTimeout(30*time.Second), // long enough that ctx is cancelled by Stop, not timeout
	)

	worker.Start(context.Background())

	// Wait for the handler to actually start before cancelling.
	select {
	case <-job.started:
	case <-time.After(5 * time.Second):
		worker.Stop()
		t.Fatalf("HandleCtx never started")
	}

	// Cancel the worker's lifecycle ctx; the per-job ctx is derived from it
	// and must observe cancellation.
	worker.Stop()

	// Stop() only waits for the worker's pump goroutines; the handler
	// goroutine running HandleCtx is detached and may not have written
	// observed yet. Poll until it does (or fail).
	waitFor(t, 5*time.Second, func() bool {
		return job.observed.Load() != nil
	})

	got, ok := job.observed.Load().(error)
	if !ok || got == nil {
		t.Fatalf("HandleCtx did not observe cancellation; observed=%v", got)
	}
	if !errors.Is(got, context.Canceled) && !errors.Is(got, context.DeadlineExceeded) {
		t.Fatalf("expected ctx.Canceled or DeadlineExceeded, got %v", got)
	}
	if atomic.LoadInt32(&handlerCalls) != 0 {
		t.Errorf("user handler should not run for HandleCtxer jobs; got %d calls", handlerCalls)
	}
}

func TestWorker_HandleOnlyJobStillRuns(t *testing.T) {
	q := NewMemoryDriver()
	q.Start()
	defer q.Shutdown(context.Background())

	processed := int32(0)
	job := &TestJob{
		ID:      "legacy-1",
		Message: "legacy job (Handle only)",
		Handler: func() error {
			atomic.AddInt32(&processed, 1)
			return nil
		},
	}

	if err := q.PushCtx(context.Background(), job, "legacy-queue"); err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	worker := NewWorker(q, "legacy-queue", func(j Job) error {
		return j.Handle()
	}, WithInterval(25*time.Millisecond), WithMaxRetries(1))

	worker.Start(context.Background())
	waitFor(t, 5*time.Second, func() bool {
		return atomic.LoadInt32(&processed) == 1
	})
	worker.Stop()

	if got := atomic.LoadInt32(&processed); got != 1 {
		t.Errorf("expected legacy job to run once, got %d", got)
	}
}

// TestWorker_ShutdownCancelledJobNotRetriedOrFailed asserts that when a
// HandleCtxer job is interrupted by worker shutdown (not by a per-job
// timeout), the worker treats it as a clean abort: the job is NOT routed
// through handleJobFailure, so:
//   - Job.Failed() is never called (failedWith stays nil).
//   - The driver's failed-job list remains empty.
//   - No JobFailed or JobRetrying events are dispatched.
//   - No retry is pushed (queue size stays 0 after the original Pop).
func TestWorker_ShutdownCancelledJobNotRetriedOrFailed(t *testing.T) {
	q := NewMemoryDriver()
	q.Start()
	defer q.Shutdown(context.Background())

	job := &ctxAwareJob{
		ID:      "shutdown-abort-1",
		started: make(chan struct{}),
	}

	if err := q.PushCtx(context.Background(), job, "shutdown-queue"); err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	// Track lifecycle events. JobFailed/JobRetrying must not fire.
	var (
		failedEvents   int32
		retryingEvents int32
	)
	worker := NewWorker(q, "shutdown-queue", func(j Job) error {
		return j.Handle()
	},
		WithInterval(25*time.Millisecond),
		WithMaxRetries(5), // plenty of retries available; we assert none are used
		WithBackoff(FixedBackoff(0)),
		WithTimeout(30*time.Second), // long enough that job-timeout cannot fire first
	)
	worker.SetEventDispatcher(func(_ context.Context, ev interface{}) error {
		switch ev.(type) {
		case *JobFailed:
			atomic.AddInt32(&failedEvents, 1)
		case *JobRetrying:
			atomic.AddInt32(&retryingEvents, 1)
		}
		return nil
	})

	worker.Start(context.Background())

	// Wait for the handler to actually start before shutting down, otherwise
	// we could race past the job and the test would be vacuous.
	select {
	case <-job.started:
	case <-time.After(5 * time.Second):
		worker.Stop()
		t.Fatalf("HandleCtx never started")
	}

	worker.Stop()

	// Confirm cancellation actually reached the handler (sanity: otherwise
	// the rest of the assertions are meaningless). The handler goroutine
	// is detached from Stop(), so poll briefly.
	waitFor(t, 5*time.Second, func() bool {
		return job.observed.Load() != nil
	})
	gotErr, _ := job.observed.Load().(error)
	if gotErr == nil {
		t.Fatalf("HandleCtx did not observe cancellation")
	}

	// Brief settle time for any (unwanted) async failure paths to fire.
	// If retries/Failed were going to happen, they'd happen well within
	// this window. We're asserting they don't.
	time.Sleep(100 * time.Millisecond)

	// No Failed() call on the job.
	if v := job.failedWith.Load(); v != nil {
		t.Errorf("Job.Failed() was called with %v on shutdown-cancelled job; expected no call", v)
	}

	// Driver's failed list stays empty.
	failed, err := q.GetFailed("shutdown-queue")
	if err != nil {
		t.Fatalf("GetFailed: %v", err)
	}
	if len(failed) != 0 {
		t.Errorf("expected 0 failed jobs after shutdown-cancellation, got %d", len(failed))
	}

	// No JobFailed / JobRetrying events fired.
	if n := atomic.LoadInt32(&failedEvents); n != 0 {
		t.Errorf("expected 0 JobFailed events, got %d", n)
	}
	if n := atomic.LoadInt32(&retryingEvents); n != 0 {
		t.Errorf("expected 0 JobRetrying events, got %d", n)
	}

	// Queue is empty (original Pop removed it; no retry was pushed).
	size, err := q.Size("shutdown-queue")
	if err != nil {
		t.Fatalf("Size: %v", err)
	}
	if size != 0 {
		t.Errorf("expected queue size 0 (no retry pushed), got %d", size)
	}
}

// recordingLogger captures WorkerLogger calls for assertion. Safe for
// concurrent use; pump goroutines and the test goroutine both call into
// it.
type recordingLogger struct {
	mu    sync.Mutex
	infos []string
	warns []string
	errs  []string
}

func (r *recordingLogger) Info(msg string, _ ...any) {
	r.mu.Lock()
	r.infos = append(r.infos, msg)
	r.mu.Unlock()
}

func (r *recordingLogger) Warn(msg string, _ ...any) {
	r.mu.Lock()
	r.warns = append(r.warns, msg)
	r.mu.Unlock()
}

func (r *recordingLogger) Error(msg string, _ ...any) {
	r.mu.Lock()
	r.errs = append(r.errs, msg)
	r.mu.Unlock()
}

func (r *recordingLogger) warnCount(substr string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, m := range r.warns {
		if strings.Contains(m, substr) {
			n++
		}
	}
	return n
}

// ctxIgnoringJob deliberately ignores ctx and blocks on an internal
// release channel. Used to simulate a misbehaving handler that does not
// honor ctx.Done(); the worker must NOT hang on it.
type ctxIgnoringJob struct {
	ID      string
	started chan struct{}
	release chan struct{}
}

func (j *ctxIgnoringJob) Handle() error {
	return errors.New("ctxIgnoringJob.Handle called instead of HandleCtx")
}

func (j *ctxIgnoringJob) HandleCtx(_ context.Context) error {
	close(j.started)
	<-j.release // ignore ctx entirely
	return nil
}

func (j *ctxIgnoringJob) Failed(error)  {}
func (j *ctxIgnoringJob) JobID() string { return j.ID }

// TestWorker_HandlerKillCeilingPreventsHang asserts the goroutine-leak
// fix from worker.go's drainHandler: when a HandleCtxer ignores ctx, the
// per-job timeout fires, the handler goroutine does NOT return, and the
// worker should NOT hang. Instead processJob returns once the kill
// ceiling expires and a WARN is logged. Stop() must complete promptly.
func TestWorker_HandlerKillCeilingPreventsHang(t *testing.T) {
	// Shrink the kill ceiling so the test runs in <1s instead of waiting
	// the production-default 5s. Restore on exit.
	prev := defaultHandlerKillCeiling
	defaultHandlerKillCeiling = 200 * time.Millisecond
	t.Cleanup(func() { defaultHandlerKillCeiling = prev })

	q := NewMemoryDriver()
	q.Start()
	defer q.Shutdown(context.Background())

	job := &ctxIgnoringJob{
		ID:      "leaky-1",
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	// Ensure the goroutine eventually exits so the test process is clean.
	// sync.Once guards against double-close from both this cleanup AND the
	// timeout-branch below.
	var releaseOnce sync.Once
	releaseJob := func() { releaseOnce.Do(func() { close(job.release) }) }
	t.Cleanup(releaseJob)

	if err := q.PushCtx(context.Background(), job, "leak-queue"); err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	rec := &recordingLogger{}
	worker := NewWorker(q, "leak-queue", func(j Job) error {
		return j.Handle()
	},
		WithInterval(25*time.Millisecond),
		WithMaxRetries(0), // no retries; we want one shot
		WithBackoff(FixedBackoff(0)),
		WithTimeout(50*time.Millisecond), // per-job timeout < kill ceiling
		WithWorkerLogger(rec),
	)

	worker.Start(context.Background())

	// Wait for handler to actually start; otherwise we'd race past it.
	select {
	case <-job.started:
	case <-time.After(5 * time.Second):
		worker.Stop()
		t.Fatalf("HandleCtx never started")
	}

	// Stop() must return promptly: bounded by kill ceiling + slack, NOT
	// blocked indefinitely on the stuck handler. If drainHandler is broken
	// or w.wg waits on the handler goroutine, this assertion fails.
	stopDone := make(chan struct{})
	stopStart := time.Now()
	go func() {
		worker.Stop()
		close(stopDone)
	}()

	maxWait := defaultHandlerKillCeiling + 2*time.Second
	select {
	case <-stopDone:
		if elapsed := time.Since(stopStart); elapsed > maxWait {
			t.Errorf("Stop took %v, expected <= %v", elapsed, maxWait)
		}
	case <-time.After(maxWait):
		// Release the stuck job so the goroutine can exit before we abort
		// the test, otherwise it leaks for the rest of the run.
		releaseJob()
		<-stopDone
		t.Fatalf("Stop did not complete within %v; drainHandler likely hung", maxWait)
	}

	// The kill-ceiling WARN must have fired at least once for this job.
	if n := rec.warnCount("Handler goroutine did not return"); n < 1 {
		t.Errorf("expected at least one kill-ceiling WARN, got %d (warns=%v)", n, rec.warns)
	}
}
