package queue

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorker(t *testing.T) {
	q := NewMemoryDriver()

	t.Run("Basic Worker", func(t *testing.T) {
		processed := int32(0)

		// Create worker
		worker := NewWorker(q, "worker-test", func(job Job) error {
			atomic.AddInt32(&processed, 1)
			return nil
		})

		// Push jobs
		for i := 0; i < 5; i++ {
			job := &TestJob{
				ID:      "worker-" + string(rune(i)),
				Message: "Worker test",
			}
			err := q.Push(job, "worker-test")
			if err != nil {
				t.Fatalf("Failed to push job: %v", err)
			}
		}

		// Start worker
		worker.Start()

		// Wait for processing
		time.Sleep(200 * time.Millisecond)

		// Stop worker
		worker.Stop()

		if atomic.LoadInt32(&processed) != 5 {
			t.Errorf("Expected 5 jobs processed, got %d", processed)
		}
	})

	t.Run("Concurrent Workers", func(t *testing.T) {
		processed := int32(0)

		// Create worker with concurrency
		worker := NewWorker(q, "concurrent-worker", func(job Job) error {
			atomic.AddInt32(&processed, 1)
			time.Sleep(10 * time.Millisecond) // Simulate work
			return nil
		}, WithConcurrency(3))

		// Push jobs
		for i := 0; i < 10; i++ {
			job := &TestJob{
				ID:      "concurrent-" + string(rune(i)),
				Message: "Concurrent worker test",
			}
			err := q.Push(job, "concurrent-worker")
			if err != nil {
				t.Fatalf("Failed to push job: %v", err)
			}
		}

		// Start worker
		worker.Start()

		// Wait for processing
		time.Sleep(500 * time.Millisecond)

		// Stop worker
		worker.Stop()

		if atomic.LoadInt32(&processed) != 10 {
			t.Errorf("Expected 10 jobs processed, got %d", processed)
		}
	})

	t.Run("Failed Jobs", func(t *testing.T) {
		processed := int32(0)
		failed := int32(0)

		// Create worker that fails every other job
		worker := NewWorker(q, "fail-worker", func(job Job) error {
			count := atomic.AddInt32(&processed, 1)
			if count%2 == 0 {
				return errors.New("simulated failure")
			}
			return nil
		}, WithMaxRetries(1))

		// Track failed jobs
		for i := 0; i < 6; i++ {
			job := &TestJob{
				ID:      "fail-" + string(rune(i)),
				Message: "Fail worker test",
				OnFail: func(err error) {
					atomic.AddInt32(&failed, 1)
				},
			}
			err := q.Push(job, "fail-worker")
			if err != nil {
				t.Fatalf("Failed to push job: %v", err)
			}
		}

		// Start worker
		worker.Start()

		// Wait for processing
		time.Sleep(300 * time.Millisecond)

		// Stop worker
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

		// Create a job that takes too long
		job := &TestJob{
			ID:      "timeout-1",
			Message: "Timeout test",
			Handler: func() error {
				time.Sleep(5 * time.Second) // Longer than 2s timeout for test mode
				return nil
			},
			OnFail: func(err error) {
				if err.Error() == "job timed out" {
					atomic.AddInt32(&timedOut, 1)
				}
			},
		}

		err := q.Push(job, "timeout-worker")
		if err != nil {
			t.Fatalf("Failed to push job: %v", err)
		}

		// Create worker with short interval to trigger test mode timeout
		worker := NewWorker(q, "timeout-worker", func(j Job) error {
			return j.Handle()
		}, WithInterval(50*time.Millisecond), WithMaxRetries(1))

		// Start worker
		worker.Start()

		// Wait for timeout
		time.Sleep(3 * time.Second)

		// Stop worker
		worker.Stop()

		if atomic.LoadInt32(&timedOut) != 1 {
			t.Errorf("Expected job to timeout, but it didn't")
		}
	})
}

func TestGlobalWorker(t *testing.T) {
	q := NewMemoryDriver()

	processed := int32(0)

	// Push jobs
	for i := 0; i < 3; i++ {
		job := &TestJob{
			ID:      "global-worker-" + string(rune(i)),
			Message: "Global worker test",
		}
		err := q.Push(job, "global-worker")
		if err != nil {
			t.Fatalf("Failed to push job: %v", err)
		}
	}

	// Start worker
	worker := NewWorker(q, "global-worker", func(job Job) error {
		atomic.AddInt32(&processed, 1)
		return nil
	}, WithConcurrency(2))
	worker.Start()

	// Wait for processing
	time.Sleep(200 * time.Millisecond)

	// Stop worker
	worker.Stop()

	if atomic.LoadInt32(&processed) != 3 {
		t.Errorf("Expected 3 jobs processed, got %d", processed)
	}
}

func TestWorker_RetryOnFailure(t *testing.T) {
	q := NewMemoryDriver()
	defer q.Close()

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

	err := q.Push(job, "retry-queue")
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

	worker.Start()
	// Need to wait for: 1st attempt (immediate) + 1st retry delay (~1s ticker) + 2nd retry delay (~1s ticker)
	time.Sleep(4 * time.Second)
	worker.Stop()

	got := atomic.LoadInt32(&attempts)
	if got != 3 {
		t.Errorf("Expected 3 attempts (2 failures + 1 success), got %d", got)
	}
}

func TestWorker_ExhaustsRetries(t *testing.T) {
	q := NewMemoryDriver()
	defer q.Close()

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

	err := q.Push(job, "exhaust-queue")
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

	worker.Start()
	// 1st attempt + 2 retries, each needs ~1s for ticker
	time.Sleep(5 * time.Second)
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
	defer q.Close()

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

	err := q.Push(job, "no-retry-queue")
	if err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	worker := NewWorker(q, "no-retry-queue", func(j Job) error {
		return j.Handle()
	},
		WithInterval(50*time.Millisecond),
		WithMaxRetries(1),
	)

	worker.Start()
	time.Sleep(500 * time.Millisecond)
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
	defer q.Close()

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

	err := q.Push(job, "decider-queue")
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

	worker.Start()
	time.Sleep(500 * time.Millisecond)
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
	defer q.Close()

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

	err := q.Push(job, "max-attempter-queue")
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

	worker.Start()
	time.Sleep(4 * time.Second)
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
	defer q.Close()

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

	err := q.Push(job, "backoffer-queue")
	if err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	worker := NewWorker(q, "backoffer-queue", func(j Job) error {
		return j.Handle()
	},
		WithInterval(50*time.Millisecond),
		WithMaxRetries(5),
	)

	worker.Start()
	time.Sleep(4 * time.Second)
	worker.Stop()

	gotAttempts := atomic.LoadInt32(&attempts)
	if gotAttempts != 3 {
		t.Errorf("Expected 3 attempts (2 failures with Backoffer delays + 1 success), got %d", gotAttempts)
	}
}
