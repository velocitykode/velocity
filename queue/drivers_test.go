package queue

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

// TestDriver runs common tests for any queue driver
func testDriver(t *testing.T, driver Driver, name string) {
	t.Run(name, func(t *testing.T) {
		t.Run("PushAndPop", func(t *testing.T) {
			job := &TestJob{
				ID:      fmt.Sprintf("%s-push-pop-1", name),
				Message: "Test message",
			}

			// Push job
			err := driver.Push(job, "test-queue")
			if err != nil {
				t.Fatalf("Failed to push job: %v", err)
			}

			// Check size
			size, err := driver.Size("test-queue")
			if err != nil {
				t.Fatalf("Failed to get queue size: %v", err)
			}
			if size != 1 {
				t.Errorf("Expected queue size 1, got %d", size)
			}

			// Pop job
			poppedJob, err := driver.Pop("test-queue")
			if err != nil {
				t.Fatalf("Failed to pop job: %v", err)
			}
			if poppedJob == nil {
				t.Fatal("Expected job, got nil")
			}

			// Check size after pop
			size, err = driver.Size("test-queue")
			if err != nil {
				t.Fatalf("Failed to get queue size: %v", err)
			}
			if size != 0 {
				t.Errorf("Expected queue size 0 after pop, got %d", size)
			}
		})

		t.Run("DelayedJob", func(t *testing.T) {
			job := &TestJob{
				ID:      fmt.Sprintf("%s-delayed-1", name),
				Message: "Delayed message",
			}

			// Push delayed job (1 second delay)
			err := driver.PushDelayed(job, 1*time.Second, "delayed-queue")
			if err != nil {
				t.Fatalf("Failed to push delayed job: %v", err)
			}

			// Should not be available immediately
			poppedJob, err := driver.Pop("delayed-queue")
			if err != nil {
				t.Fatalf("Failed to pop job: %v", err)
			}
			if poppedJob != nil {
				t.Error("Job should not be available immediately")
			}

			// Wait for delay plus processing time
			time.Sleep(2 * time.Second)

			// Should be available now
			poppedJob, err = driver.Pop("delayed-queue")
			if err != nil {
				t.Fatalf("Failed to pop job after delay: %v", err)
			}
			if poppedJob == nil {
				t.Error("Job should be available after delay")
			}
		})

		t.Run("MultipleQueues", func(t *testing.T) {
			// Push to different queues
			job1 := &TestJob{ID: fmt.Sprintf("%s-q1", name), Message: "Queue 1"}
			job2 := &TestJob{ID: fmt.Sprintf("%s-q2", name), Message: "Queue 2"}

			err := driver.Push(job1, "queue1")
			if err != nil {
				t.Fatalf("Failed to push to queue1: %v", err)
			}

			err = driver.Push(job2, "queue2")
			if err != nil {
				t.Fatalf("Failed to push to queue2: %v", err)
			}

			// Check sizes
			size1, _ := driver.Size("queue1")
			size2, _ := driver.Size("queue2")

			if size1 != 1 || size2 != 1 {
				t.Errorf("Expected both queues to have size 1, got %d and %d", size1, size2)
			}

			// Pop from specific queue
			popped1, _ := driver.Pop("queue1")
			if popped1 == nil {
				t.Error("Failed to pop from queue1")
			}

			// Check queue1 is empty but queue2 still has job
			size1, _ = driver.Size("queue1")
			size2, _ = driver.Size("queue2")

			if size1 != 0 || size2 != 1 {
				t.Errorf("Expected queue1=0, queue2=1, got %d and %d", size1, size2)
			}

			// Clean up
			driver.Clear("queue2")
		})

		t.Run("ClearQueue", func(t *testing.T) {
			// Push multiple jobs
			for i := 0; i < 5; i++ {
				job := &TestJob{
					ID:      fmt.Sprintf("%s-clear-%d", name, i),
					Message: "Clear test",
				}
				err := driver.Push(job, "clear-queue")
				if err != nil {
					t.Fatalf("Failed to push job: %v", err)
				}
			}

			// Check size
			size, err := driver.Size("clear-queue")
			if err != nil {
				t.Fatalf("Failed to get queue size: %v", err)
			}
			if size != 5 {
				t.Errorf("Expected queue size 5, got %d", size)
			}

			// Clear queue
			err = driver.Clear("clear-queue")
			if err != nil {
				t.Fatalf("Failed to clear queue: %v", err)
			}

			// Check size after clear
			size, err = driver.Size("clear-queue")
			if err != nil {
				t.Fatalf("Failed to get queue size: %v", err)
			}
			if size != 0 {
				t.Errorf("Expected queue size 0 after clear, got %d", size)
			}
		})

		t.Run("ConcurrentPushPop", func(t *testing.T) {
			var wg sync.WaitGroup
			numWorkers := 5
			numJobs := 20

			// Concurrent pushes
			for i := 0; i < numWorkers; i++ {
				wg.Add(1)
				go func(workerID int) {
					defer wg.Done()
					for j := 0; j < numJobs; j++ {
						job := &TestJob{
							ID:      fmt.Sprintf("%s-concurrent-%d-%d", name, workerID, j),
							Message: "Concurrent test",
						}
						if err := driver.Push(job, "concurrent"); err != nil {
							t.Errorf("Worker %d failed to push job %d: %v", workerID, j, err)
						}
					}
				}(i)
			}

			wg.Wait()

			// Check total size
			size, err := driver.Size("concurrent")
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
						job, err := driver.Pop("concurrent")
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
		})

		// Clean up any remaining jobs
		driver.Clear("test-queue")
		driver.Clear("delayed-queue")
		driver.Clear("queue1")
		driver.Clear("queue2")
		driver.Clear("clear-queue")
		driver.Clear("concurrent")
	})
}

// TestAllDrivers tests all available queue drivers
func TestAllDrivers(t *testing.T) {
	// Test Memory Driver
	t.Run("MemoryDriver", func(t *testing.T) {
		driver := NewMemoryDriver()
		driver.Start()
		testDriver(t, driver, "Memory")
	})

	// Database driver is covered by TestIntegrationDatabaseDriver (PostgreSQL-gated).

	// Test Redis Driver (if Redis is available)
	t.Run("RedisDriver", func(t *testing.T) {
		// Skip if no Redis configuration
		if os.Getenv("QUEUE_REDIS_HOST") == "" {
			t.Skip("Skipping Redis driver test: QUEUE_REDIS_HOST not set")
		}

		config := RedisConfig{
			Host:     getEnvOrDefault("QUEUE_REDIS_HOST", "localhost"),
			Port:     getEnvOrDefault("QUEUE_REDIS_PORT", "6379"),
			Password: os.Getenv("QUEUE_REDIS_PASSWORD"),
			DB:       getEnvOrDefault("QUEUE_REDIS_DB", "0"),
		}

		driver, err := NewRedisDriver(config)
		if err != nil {
			t.Skipf("Skipping Redis driver test: cannot connect to Redis: %v", err)
		}

		// Clear any existing data
		driver.Clear("test-queue")
		driver.Clear("delayed-queue")
		driver.Clear("queue1")
		driver.Clear("queue2")
		driver.Clear("clear-queue")
		driver.Clear("concurrent")

		testDriver(t, driver, "Redis")
	})
}

// TestDriverConfiguration tests that drivers can be configured from environment
func TestDriverConfiguration(t *testing.T) {
	t.Run("MemoryDriverConfig", func(t *testing.T) {
		// Memory driver has no configuration
		driver := NewMemoryDriver()
		driver.Start()
		if driver == nil {
			t.Fatal("Failed to create memory driver")
		}
	})

	t.Run("DatabaseDriverConfig", func(t *testing.T) {
		// Database driver uses ORM configuration
		driver := NewDatabaseDriver(nil, "")
		if driver == nil {
			t.Fatal("Failed to create database driver")
		}
	})

	t.Run("RedisDriverConfig", func(t *testing.T) {
		// Test with explicit config
		config := RedisConfig{
			Host:     "localhost",
			Port:     "6379",
			Password: "",
			DB:       "0",
		}

		// This will fail if Redis is not running, which is expected
		_, err := NewRedisDriver(config)
		if err != nil {
			// Expected if Redis is not running
			t.Logf("Redis driver creation failed (expected if Redis not running): %v", err)
		}
	})
}

// BenchmarkDrivers benchmarks all available drivers
func BenchmarkDrivers(b *testing.B) {
	// Benchmark Memory Driver
	b.Run("MemoryDriver", func(b *testing.B) {
		driver := NewMemoryDriver()
		driver.Start()
		benchmarkDriver(b, driver)
	})

	// Add Database and Redis benchmarks if available
	// Similar to TestAllDrivers but with benchmarkDriver
}

func benchmarkDriver(b *testing.B, driver Driver) {
	b.Run("Push", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			job := &TestJob{
				ID:      fmt.Sprintf("bench-%d", i),
				Message: "Benchmark",
			}
			_ = driver.Push(job, "bench-queue")
		}
		b.StopTimer()
		driver.Clear("bench-queue")
	})

	b.Run("Pop", func(b *testing.B) {
		// Pre-populate queue
		for i := 0; i < b.N; i++ {
			job := &TestJob{
				ID:      fmt.Sprintf("bench-%d", i),
				Message: "Benchmark",
			}
			_ = driver.Push(job, "bench-queue")
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, _ = driver.Pop("bench-queue")
		}
		b.StopTimer()
		driver.Clear("bench-queue")
	})
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
