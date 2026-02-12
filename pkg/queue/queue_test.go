package queue

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// TestJob is a simple job implementation for testing
type TestJob struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	Handler func() error
	OnFail  func(error)
}

func (t *TestJob) Handle() error {
	if t.Handler != nil {
		return t.Handler()
	}
	return nil
}

func (t *TestJob) Failed(err error) {
	if t.OnFail != nil {
		t.OnFail(err)
	}
}

func init() {
	// Register the TestJob type for deserialization
	Register("*queue.TestJob", func(data []byte) (Job, error) {
		var job TestJob
		// In a real implementation, we would unmarshal the data
		return &job, nil
	})
}

func TestMemoryQueue(t *testing.T) {
	q := NewMemoryQueue()

	t.Run("Push and Pop", func(t *testing.T) {
		job := &TestJob{
			ID:      "test-1",
			Message: "Test message",
		}

		// Push job
		err := q.Push(job, "test-queue")
		if err != nil {
			t.Fatalf("Failed to push job: %v", err)
		}

		// Check size
		size, err := q.Size("test-queue")
		if err != nil {
			t.Fatalf("Failed to get queue size: %v", err)
		}
		if size != 1 {
			t.Errorf("Expected queue size 1, got %d", size)
		}

		// Pop job
		poppedJob, err := q.Pop("test-queue")
		if err != nil {
			t.Fatalf("Failed to pop job: %v", err)
		}
		if poppedJob == nil {
			t.Fatal("Expected job, got nil")
		}

		// Check size after pop
		size, err = q.Size("test-queue")
		if err != nil {
			t.Fatalf("Failed to get queue size: %v", err)
		}
		if size != 0 {
			t.Errorf("Expected queue size 0 after pop, got %d", size)
		}
	})

	t.Run("Delayed Job", func(t *testing.T) {
		job := &TestJob{
			ID:      "delayed-1",
			Message: "Delayed message",
		}

		// Push delayed job (use 1 second delay since the ticker runs every second)
		err := q.PushDelayed(job, 1*time.Second, "delayed-queue")
		if err != nil {
			t.Fatalf("Failed to push delayed job: %v", err)
		}

		// Should not be available immediately
		poppedJob, err := q.Pop("delayed-queue")
		if err != nil {
			t.Fatalf("Failed to pop job: %v", err)
		}
		if poppedJob != nil {
			t.Error("Job should not be available immediately")
		}

		// Wait for delay plus processing time
		time.Sleep(2 * time.Second)

		// Should be available now
		poppedJob, err = q.Pop("delayed-queue")
		if err != nil {
			t.Fatalf("Failed to pop job after delay: %v", err)
		}
		if poppedJob == nil {
			t.Error("Job should be available after delay")
		}
	})

	t.Run("Failed Job", func(t *testing.T) {
		failedCalled := false
		job := &TestJob{
			ID:      "fail-1",
			Message: "Failing job",
			OnFail: func(err error) {
				failedCalled = true
			},
		}

		// Push job
		err := q.Push(job, "fail-queue")
		if err != nil {
			t.Fatalf("Failed to push job: %v", err)
		}

		// Mark as failed
		err = q.Failed(job, errors.New("test error"), "fail-queue")
		if err != nil {
			t.Fatalf("Failed to mark job as failed: %v", err)
		}

		if !failedCalled {
			t.Error("Failed callback not called")
		}
	})

	t.Run("Clear Queue", func(t *testing.T) {
		// Push multiple jobs
		for i := 0; i < 5; i++ {
			job := &TestJob{
				ID:      "clear-" + string(rune(i)),
				Message: "Clear test",
			}
			err := q.Push(job, "clear-queue")
			if err != nil {
				t.Fatalf("Failed to push job: %v", err)
			}
		}

		// Check size
		size, err := q.Size("clear-queue")
		if err != nil {
			t.Fatalf("Failed to get queue size: %v", err)
		}
		if size != 5 {
			t.Errorf("Expected queue size 5, got %d", size)
		}

		// Clear queue
		err = q.Clear("clear-queue")
		if err != nil {
			t.Fatalf("Failed to clear queue: %v", err)
		}

		// Check size after clear
		size, err = q.Size("clear-queue")
		if err != nil {
			t.Fatalf("Failed to get queue size: %v", err)
		}
		if size != 0 {
			t.Errorf("Expected queue size 0 after clear, got %d", size)
		}
	})
}

func TestConcurrentAccess(t *testing.T) {
	q := NewMemoryQueue()
	var wg sync.WaitGroup
	numWorkers := 10
	numJobs := 100

	// Concurrent pushes
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < numJobs; j++ {
				job := &TestJob{
					ID:      string(rune(workerID)) + "-" + string(rune(j)),
					Message: "Concurrent test",
				}
				if err := q.Push(job, "concurrent"); err != nil {
					t.Errorf("Worker %d failed to push job %d: %v", workerID, j, err)
				}
			}
		}(i)
	}

	wg.Wait()

	// Check total size
	size, err := q.Size("concurrent")
	if err != nil {
		t.Fatalf("Failed to get queue size: %v", err)
	}
	expectedSize := int64(numWorkers * numJobs)
	if size != expectedSize {
		t.Errorf("Expected queue size %d, got %d", expectedSize, size)
	}

	// Concurrent pops
	popped := int64(0)
	var mu sync.Mutex

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for {
				job, err := q.Pop("concurrent")
				if err != nil {
					t.Errorf("Worker %d failed to pop: %v", workerID, err)
					return
				}
				if job == nil {
					return // No more jobs
				}
				mu.Lock()
				popped++
				mu.Unlock()
			}
		}(i)
	}

	wg.Wait()

	if popped != expectedSize {
		t.Errorf("Expected to pop %d jobs, got %d", expectedSize, popped)
	}
}

func TestInstanceAPI(t *testing.T) {
	q := NewMemoryQueue()

	t.Run("Push and Pop", func(t *testing.T) {
		job := &TestJob{
			ID:      "instance-1",
			Message: "Instance test",
		}

		// Push using instance API
		err := q.Push(job, "instance-queue")
		if err != nil {
			t.Fatalf("Failed to push job: %v", err)
		}

		// Check size
		size, err := q.Size("instance-queue")
		if err != nil {
			t.Fatalf("Failed to get queue size: %v", err)
		}
		if size != 1 {
			t.Errorf("Expected queue size 1, got %d", size)
		}

		// Pop using instance API
		poppedJob, err := q.Pop("instance-queue")
		if err != nil {
			t.Fatalf("Failed to pop job: %v", err)
		}
		if poppedJob == nil {
			t.Fatal("Expected job, got nil")
		}
	})

	t.Run("Delayed", func(t *testing.T) {
		job := &TestJob{
			ID:      "instance-delayed-1",
			Message: "Instance delayed test",
		}

		// Push delayed using instance API (use 1 second delay for consistency)
		err := q.PushDelayed(job, 1*time.Second, "instance-delayed")
		if err != nil {
			t.Fatalf("Failed to push delayed job: %v", err)
		}

		// Wait and pop
		time.Sleep(2 * time.Second)

		poppedJob, err := q.Pop("instance-delayed")
		if err != nil {
			t.Fatalf("Failed to pop job: %v", err)
		}
		if poppedJob == nil {
			t.Fatal("Expected job after delay, got nil")
		}
	})

	t.Run("Clear", func(t *testing.T) {
		// Push jobs
		for i := 0; i < 3; i++ {
			job := &TestJob{
				ID:      "instance-clear-" + string(rune(i)),
				Message: "Clear test",
			}
			err := q.Push(job, "instance-clear")
			if err != nil {
				t.Fatalf("Failed to push job: %v", err)
			}
		}

		// Clear using instance API
		err := q.Clear("instance-clear")
		if err != nil {
			t.Fatalf("Failed to clear queue: %v", err)
		}

		// Check size
		size, err := q.Size("instance-clear")
		if err != nil {
			t.Fatalf("Failed to get queue size: %v", err)
		}
		if size != 0 {
			t.Errorf("Expected queue size 0 after clear, got %d", size)
		}
	})
}
