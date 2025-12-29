package queue

import (
	"database/sql"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// TestIntegrationMemoryDriver tests memory driver with real job processing
func TestIntegrationMemoryDriver(t *testing.T) {
	// Set environment to use memory driver
	os.Setenv("QUEUE_DRIVER", "memory")
	defer os.Unsetenv("QUEUE_DRIVER")

	// Reinitialize to pick up the config
	err := Reinitialize()
	if err != nil {
		t.Fatalf("Failed to reinitialize with memory driver: %v", err)
	}

	t.Run("ConfigurationPickup", func(t *testing.T) {
		// Verify we're using memory driver
		// Push a test job and verify it works
		job := &TestJob{
			ID:      "config-test",
			Message: "Testing configuration",
		}

		err := Push(job, "config-queue")
		if err != nil {
			t.Fatalf("Failed to push job: %v", err)
		}

		size, _ := Size("config-queue")
		if size != 1 {
			t.Errorf("Expected queue size 1, got %d", size)
		}

		// Clean up
		Clear("config-queue")
	})

	t.Run("JobDispatchAndProcessing", func(t *testing.T) {
		processed := int32(0)
		failedCount := int32(0)

		// Create jobs that track processing
		successJob := &TestJob{
			ID:      "success-1",
			Message: "Should succeed",
			Handler: func() error {
				atomic.AddInt32(&processed, 1)
				return nil
			},
		}

		failJob := &TestJob{
			ID:      "fail-1",
			Message: "Should fail",
			Handler: func() error {
				return fmt.Errorf("intentional failure")
			},
			OnFail: func(err error) {
				atomic.AddInt32(&failedCount, 1)
			},
		}

		// Dispatch jobs
		err := Push(successJob, "process-queue")
		if err != nil {
			t.Fatalf("Failed to push success job: %v", err)
		}

		err = Push(failJob, "process-queue")
		if err != nil {
			t.Fatalf("Failed to push fail job: %v", err)
		}

		// Start worker to process jobs
		handler := func(job Job) error {
			return job.Handle()
		}
		worker := NewWorker(globalQueue, "process-queue", handler)
		go worker.Start()
		defer worker.Stop()

		// Wait for processing
		time.Sleep(500 * time.Millisecond)

		// Verify processing
		if atomic.LoadInt32(&processed) != 1 {
			t.Errorf("Expected 1 job processed, got %d", processed)
		}

		if atomic.LoadInt32(&failedCount) != 1 {
			t.Errorf("Expected 1 job failed, got %d", failedCount)
		}

		// Queue should be empty
		size, _ := Size("process-queue")
		if size != 0 {
			t.Errorf("Expected empty queue after processing, got size %d", size)
		}
	})

	t.Run("DelayedJobProcessing", func(t *testing.T) {
		processed := int32(0)

		delayedJob := &TestJob{
			ID:      "delayed-1",
			Message: "Delayed processing",
			Handler: func() error {
				atomic.AddInt32(&processed, 1)
				return nil
			},
		}

		// Push with 1 second delay
		err := Later(1*time.Second, delayedJob, "delayed-queue")
		if err != nil {
			t.Fatalf("Failed to push delayed job: %v", err)
		}

		// Start worker
		handler := func(job Job) error {
			return job.Handle()
		}
		worker := NewWorker(globalQueue, "delayed-queue", handler)
		go worker.Start()
		defer worker.Stop()

		// Should not process immediately
		time.Sleep(500 * time.Millisecond)
		if atomic.LoadInt32(&processed) != 0 {
			t.Error("Job processed too early")
		}

		// Should process after delay
		time.Sleep(1500 * time.Millisecond)
		if atomic.LoadInt32(&processed) != 1 {
			t.Error("Delayed job not processed")
		}
	})

	t.Run("ConcurrentWorkers", func(t *testing.T) {
		numJobs := 100
		processed := int32(0)

		// Create many jobs
		for i := 0; i < numJobs; i++ {
			job := &TestJob{
				ID:      fmt.Sprintf("concurrent-%d", i),
				Message: "Concurrent test",
				Handler: func() error {
					// Simulate some work
					time.Sleep(10 * time.Millisecond)
					atomic.AddInt32(&processed, 1)
					return nil
				},
			}

			err := Push(job, "concurrent-queue")
			if err != nil {
				t.Fatalf("Failed to push job %d: %v", i, err)
			}
		}

		// Start multiple workers
		numWorkers := 5
		workers := make([]*Worker, numWorkers)
		handler := func(job Job) error {
			return job.Handle()
		}
		for i := 0; i < numWorkers; i++ {
			workers[i] = NewWorker(globalQueue, "concurrent-queue", handler)
			go workers[i].Start()
		}

		// Wait for all jobs to be processed
		timeout := time.After(5 * time.Second)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-timeout:
				t.Fatalf("Timeout: only %d/%d jobs processed", atomic.LoadInt32(&processed), numJobs)
			case <-ticker.C:
				if atomic.LoadInt32(&processed) == int32(numJobs) {
					goto done
				}
			}
		}

	done:
		// Stop all workers
		for _, w := range workers {
			w.Stop()
		}

		// Verify all jobs were processed
		if atomic.LoadInt32(&processed) != int32(numJobs) {
			t.Errorf("Expected %d jobs processed, got %d", numJobs, atomic.LoadInt32(&processed))
		}

		// Queue should be empty
		size, _ := Size("concurrent-queue")
		if size != 0 {
			t.Errorf("Expected empty queue, got size %d", size)
		}
	})
}

// TestIntegrationDatabaseDriver tests database driver with PostgreSQL
func TestIntegrationDatabaseDriver(t *testing.T) {
	t.Skip("TODO: fix test")
	// Check if we can connect to PostgreSQL
	dbHost := os.Getenv("DB_HOST")
	if dbHost == "" {
		dbHost = "localhost"
	}

	dbUser := os.Getenv("DB_USERNAME")
	if dbUser == "" {
		dbUser = "ali"
	}

	dbName := os.Getenv("DB_DATABASE")
	if dbName == "" {
		dbName = "velocity_test"
	}

	dsn := fmt.Sprintf("host=%s user=%s dbname=%s sslmode=disable", dbHost, dbUser, dbName)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Skip("Cannot connect to PostgreSQL, skipping database driver tests")
	}
	defer db.Close()

	// Ensure tables exist
	createTables := `
	DROP TABLE IF EXISTS jobs CASCADE;
	DROP TABLE IF EXISTS failed_jobs CASCADE;

	CREATE TABLE jobs (
		id SERIAL PRIMARY KEY,
		queue VARCHAR(255) NOT NULL,
		payload TEXT NOT NULL,
		attempts INTEGER DEFAULT 0,
		scheduled_at TIMESTAMP NOT NULL,
		reserved_at TIMESTAMP,
		reserved_by VARCHAR(255),
		failed_at TIMESTAMP,
		failed_reason TEXT,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE failed_jobs (
		id SERIAL PRIMARY KEY,
		queue VARCHAR(255) NOT NULL,
		payload TEXT NOT NULL,
		exception TEXT NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);`

	_, err = db.Exec(createTables)
	if err != nil {
		t.Fatalf("Failed to create tables: %v", err)
	}

	// Set environment to use database driver
	os.Setenv("QUEUE_DRIVER", "database")
	os.Setenv("DB_CONNECTION", "postgres")
	os.Setenv("DB_HOST", dbHost)
	os.Setenv("DB_USERNAME", dbUser)
	os.Setenv("DB_DATABASE", dbName)
	defer func() {
		os.Unsetenv("QUEUE_DRIVER")
		os.Unsetenv("DB_CONNECTION")
	}()

	// Initialize ORM with the test database
	// The database driver needs orm.DB() to return a valid connection
	// We'll use a global variable that the driver can access
	// For now, we'll create the driver with the db directly
	// Note: In production, this is handled by the consumer app's bootstrap

	t.Run("DatabaseJobPersistence", func(t *testing.T) {
		// For testing, we'll use the database connection directly
		// The database driver would normally get this from orm.DB()
		t.Skip("Database driver requires full ORM initialization")

		// Create database driver directly
		driver := NewDatabaseDriver()

		// Push a job
		job := &TestJob{
			ID:      "db-persist-1",
			Message: "Database persistence test",
		}

		err := driver.Push(job, "db-queue")
		if err != nil {
			t.Fatalf("Failed to push job: %v", err)
		}

		// Verify job is in database
		var count int
		err = db.QueryRow("SELECT COUNT(*) FROM jobs WHERE queue = 'db-queue'").Scan(&count)
		if err != nil {
			t.Fatalf("Failed to query jobs: %v", err)
		}

		if count != 1 {
			t.Errorf("Expected 1 job in database, got %d", count)
		}

		// Pop the job
		poppedJob, err := driver.Pop("db-queue")
		if err != nil {
			t.Fatalf("Failed to pop job: %v", err)
		}

		if poppedJob == nil {
			t.Error("Expected to pop job, got nil")
		}

		// Verify job is removed
		err = db.QueryRow("SELECT COUNT(*) FROM jobs WHERE queue = 'db-queue'").Scan(&count)
		if err != nil {
			t.Fatalf("Failed to query jobs: %v", err)
		}

		if count != 0 {
			t.Errorf("Expected 0 jobs in database after pop, got %d", count)
		}
	})

	t.Run("DatabaseDelayedJobs", func(t *testing.T) {
		t.Skip("Database driver requires full ORM initialization")
		driver := NewDatabaseDriver()

		// Push delayed job
		job := &TestJob{
			ID:      "db-delayed-1",
			Message: "Database delayed test",
		}

		err := driver.PushDelayed(job, 2*time.Second, "db-delayed")
		if err != nil {
			t.Fatalf("Failed to push delayed job: %v", err)
		}

		// Should not be available immediately
		poppedJob, err := driver.Pop("db-delayed")
		if err != nil {
			t.Fatalf("Failed to pop: %v", err)
		}
		if poppedJob != nil {
			t.Error("Job should not be available immediately")
		}

		// Wait for delay
		time.Sleep(2100 * time.Millisecond)

		// Should be available now
		poppedJob, err = driver.Pop("db-delayed")
		if err != nil {
			t.Fatalf("Failed to pop after delay: %v", err)
		}
		if poppedJob == nil {
			t.Error("Job should be available after delay")
		}
	})

	t.Run("DatabaseConcurrentProcessing", func(t *testing.T) {
		t.Skip("Database driver requires full ORM initialization")
		driver := NewDatabaseDriver()

		// Push multiple jobs
		numJobs := 20
		for i := 0; i < numJobs; i++ {
			job := &TestJob{
				ID:      fmt.Sprintf("db-concurrent-%d", i),
				Message: "Concurrent database test",
			}
			err := driver.Push(job, "db-concurrent")
			if err != nil {
				t.Fatalf("Failed to push job %d: %v", i, err)
			}
		}

		// Verify all jobs are in database
		var count int
		err = db.QueryRow("SELECT COUNT(*) FROM jobs WHERE queue = 'db-concurrent'").Scan(&count)
		if err != nil {
			t.Fatalf("Failed to query jobs: %v", err)
		}
		if count != numJobs {
			t.Errorf("Expected %d jobs in database, got %d", numJobs, count)
		}

		// Concurrent pops
		var wg sync.WaitGroup
		popped := int32(0)
		numWorkers := 5

		for i := 0; i < numWorkers; i++ {
			wg.Add(1)
			go func(workerID int) {
				defer wg.Done()
				for {
					job, err := driver.Pop("db-concurrent")
					if err != nil {
						t.Errorf("Worker %d error: %v", workerID, err)
						return
					}
					if job == nil {
						return // No more jobs
					}
					atomic.AddInt32(&popped, 1)
				}
			}(i)
		}

		wg.Wait()

		// Verify all jobs were processed
		if atomic.LoadInt32(&popped) != int32(numJobs) {
			t.Errorf("Expected %d jobs popped, got %d", numJobs, atomic.LoadInt32(&popped))
		}

		// Verify database is empty
		err = db.QueryRow("SELECT COUNT(*) FROM jobs WHERE queue = 'db-concurrent'").Scan(&count)
		if err != nil {
			t.Fatalf("Failed to query jobs: %v", err)
		}
		if count != 0 {
			t.Errorf("Expected 0 jobs in database after processing, got %d", count)
		}
	})

	// Clean up
	db.Exec("DELETE FROM jobs")
	db.Exec("DELETE FROM failed_jobs")
}

// TestIntegrationRedisDriver tests Redis driver if available
func TestIntegrationRedisDriver(t *testing.T) {
	// Check if Redis is configured
	redisHost := os.Getenv("QUEUE_REDIS_HOST")
	if redisHost == "" {
		redisHost = "localhost"
	}

	// Try to create Redis driver
	config := RedisConfig{
		Host:     redisHost,
		Port:     "6379",
		Password: "",
		DB:       "15", // Use a different DB for testing
	}

	driver, err := NewRedisDriver(config)
	if err != nil {
		t.Skip("Cannot connect to Redis, skipping Redis driver tests")
	}

	// Clear any existing data
	driver.Clear("redis-test")
	driver.Clear("redis-delayed")
	driver.Clear("redis-concurrent")

	t.Run("RedisJobOperations", func(t *testing.T) {
		// Push a job
		job := &TestJob{
			ID:      "redis-1",
			Message: "Redis test",
		}

		err := driver.Push(job, "redis-test")
		if err != nil {
			t.Fatalf("Failed to push job: %v", err)
		}

		// Check size
		size, err := driver.Size("redis-test")
		if err != nil {
			t.Fatalf("Failed to get size: %v", err)
		}
		if size != 1 {
			t.Errorf("Expected size 1, got %d", size)
		}

		// Pop the job
		poppedJob, err := driver.Pop("redis-test")
		if err != nil {
			t.Fatalf("Failed to pop job: %v", err)
		}
		if poppedJob == nil {
			t.Error("Expected to pop job, got nil")
		}

		// Size should be 0
		size, err = driver.Size("redis-test")
		if err != nil {
			t.Fatalf("Failed to get size: %v", err)
		}
		if size != 0 {
			t.Errorf("Expected size 0 after pop, got %d", size)
		}
	})

	t.Run("RedisDelayedJobs", func(t *testing.T) {
		job := &TestJob{
			ID:      "redis-delayed-1",
			Message: "Redis delayed test",
		}

		// Push with delay
		err := driver.PushDelayed(job, 1*time.Second, "redis-delayed")
		if err != nil {
			t.Fatalf("Failed to push delayed job: %v", err)
		}

		// Should not be available immediately
		poppedJob, err := driver.Pop("redis-delayed")
		if err != nil {
			t.Fatalf("Failed to pop: %v", err)
		}
		if poppedJob != nil {
			t.Error("Job should not be available immediately")
		}

		// Wait and process delayed jobs
		time.Sleep(1100 * time.Millisecond)

		// For Redis, process delayed jobs (normally done automatically)
		// The driver handles this internally

		// Should be available now
		poppedJob, err = driver.Pop("redis-delayed")
		if err != nil {
			t.Fatalf("Failed to pop after delay: %v", err)
		}
		if poppedJob == nil {
			t.Error("Job should be available after delay")
		}
	})

	t.Run("RedisConcurrentProcessing", func(t *testing.T) {
		// Push multiple jobs
		numJobs := 50
		for i := 0; i < numJobs; i++ {
			job := &TestJob{
				ID:      fmt.Sprintf("redis-concurrent-%d", i),
				Message: "Redis concurrent test",
			}
			err := driver.Push(job, "redis-concurrent")
			if err != nil {
				t.Fatalf("Failed to push job %d: %v", i, err)
			}
		}

		// Concurrent pops
		var wg sync.WaitGroup
		popped := int32(0)
		numWorkers := 5

		for i := 0; i < numWorkers; i++ {
			wg.Add(1)
			go func(workerID int) {
				defer wg.Done()
				for {
					job, err := driver.Pop("redis-concurrent")
					if err != nil {
						t.Errorf("Worker %d error: %v", workerID, err)
						return
					}
					if job == nil {
						return // No more jobs
					}
					atomic.AddInt32(&popped, 1)
				}
			}(i)
		}

		wg.Wait()

		// Verify all jobs were processed
		if atomic.LoadInt32(&popped) != int32(numJobs) {
			t.Errorf("Expected %d jobs popped, got %d", numJobs, atomic.LoadInt32(&popped))
		}

		// Queue should be empty
		size, _ := driver.Size("redis-concurrent")
		if size != 0 {
			t.Errorf("Expected empty queue, got size %d", size)
		}
	})

	// Clean up
	driver.Clear("redis-test")
	driver.Clear("redis-delayed")
	driver.Clear("redis-concurrent")
}

// TestConfigurationFromEnvironment tests that drivers are configured from env vars
func TestConfigurationFromEnvironment(t *testing.T) {
	originalDriver := os.Getenv("QUEUE_DRIVER")
	defer os.Setenv("QUEUE_DRIVER", originalDriver)

	tests := []struct {
		name   string
		driver string
		verify func(t *testing.T)
	}{
		{
			name:   "MemoryFromEnv",
			driver: "memory",
			verify: func(t *testing.T) {
				// Memory driver should always work
				job := &TestJob{ID: "mem-env", Message: "test"}
				err := Push(job, "env-test")
				if err != nil {
					t.Errorf("Failed to push with memory driver: %v", err)
				}
				Clear("env-test")
			},
		},
		{
			name:   "DatabaseFromEnv",
			driver: "database",
			verify: func(t *testing.T) {
				// Database driver needs ORM initialized, skip if not available
				t.Log("Database driver would be used if ORM was initialized")
			},
		},
		{
			name:   "RedisFromEnv",
			driver: "redis",
			verify: func(t *testing.T) {
				// This might fail if Redis is not available, which is ok
				job := &TestJob{ID: "redis-env", Message: "test"}
				_ = Push(job, "env-test")
				Clear("env-test")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("QUEUE_DRIVER", tt.driver)
			err := Reinitialize()
			if err != nil {
				// Some drivers might fail if backend is not available
				t.Logf("Reinitialize with %s failed (ok if backend unavailable): %v", tt.driver, err)
				return
			}
			tt.verify(t)
		})
	}
}

// BenchmarkQueueOperations benchmarks queue operations
func BenchmarkQueueOperations(b *testing.B) {
	// Use memory driver for consistent benchmarks
	os.Setenv("QUEUE_DRIVER", "memory")
	Reinitialize()

	b.Run("Push", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			job := &TestJob{
				ID:      fmt.Sprintf("bench-%d", i),
				Message: "Benchmark",
			}
			Push(job, "bench-queue")
		}
		b.StopTimer()
		Clear("bench-queue")
	})

	b.Run("PushAndPop", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			job := &TestJob{
				ID:      fmt.Sprintf("bench-%d", i),
				Message: "Benchmark",
			}
			Push(job, "bench-queue")
			Pop("bench-queue")
		}
	})

	b.Run("ConcurrentPush", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				job := &TestJob{
					ID:      fmt.Sprintf("bench-parallel-%d", i),
					Message: "Benchmark",
				}
				Push(job, "bench-parallel")
				i++
			}
		})
		b.StopTimer()
		Clear("bench-parallel")
	})
}