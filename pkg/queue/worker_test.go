package queue

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorker(t *testing.T) {
	q := NewMemoryQueue()
	SetDefault(q)

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
		})

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
		}, WithInterval(50*time.Millisecond))

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
	q := NewMemoryQueue()
	SetDefault(q)

	processed := int32(0)

	// Push jobs
	for i := 0; i < 3; i++ {
		job := &TestJob{
			ID:      "global-worker-" + string(rune(i)),
			Message: "Global worker test",
		}
		err := Push(job, "global-worker")
		if err != nil {
			t.Fatalf("Failed to push job: %v", err)
		}
	}

	// Start worker using global API
	worker := Work("global-worker", func(job Job) error {
		atomic.AddInt32(&processed, 1)
		return nil
	}, WithConcurrency(2))

	// Wait for processing
	time.Sleep(200 * time.Millisecond)

	// Stop worker
	worker.Stop()

	if atomic.LoadInt32(&processed) != 3 {
		t.Errorf("Expected 3 jobs processed, got %d", processed)
	}
}
