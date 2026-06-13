package log

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewManager(t *testing.T) {
	cfg := LoggingConfig{
		Default: "console",
		Channels: map[string]ChannelConfig{
			"console": {
				Driver: "console",
				Level:  "debug",
			},
		},
	}

	manager := NewManager(cfg)
	if manager == nil {
		t.Fatal("NewManager() returned nil")
	}
	if manager.config.Default != "console" {
		t.Errorf("NewManager() default = %v, want console", manager.config.Default)
	}
}

func TestManager_Channel(t *testing.T) {
	cfg := LoggingConfig{
		Default: "console",
		Channels: map[string]ChannelConfig{
			"console": {
				Driver: "console",
				Level:  "debug",
			},
			"file": {
				Driver: "file",
				Level:  "info",
				Path:   "/tmp/test-logs",
			},
			"null": {
				Driver: "null",
			},
		},
	}

	manager := NewManager(cfg)

	// Test getting console channel
	console, err := manager.Channel("console")
	if err != nil {
		t.Errorf("Channel(console) error = %v", err)
	}
	if console == nil {
		t.Error("Channel(console) returned nil")
	}

	// Test getting file channel
	file, err := manager.Channel("file")
	if err != nil {
		t.Errorf("Channel(file) error = %v", err)
	}
	if file == nil {
		t.Error("Channel(file) returned nil")
	}

	// Test getting null channel
	null, err := manager.Channel("null")
	if err != nil {
		t.Errorf("Channel(null) error = %v", err)
	}
	if null == nil {
		t.Error("Channel(null) returned nil")
	}

	// Test getting non-existent channel
	_, err = manager.Channel("nonexistent")
	if err == nil {
		t.Error("Channel(nonexistent) should return error")
	}
}

func TestManager_Default(t *testing.T) {
	cfg := LoggingConfig{
		Default: "console",
		Channels: map[string]ChannelConfig{
			"console": {
				Driver: "console",
				Level:  "debug",
			},
		},
	}

	manager := NewManager(cfg)

	logger, err := manager.Default()
	if err != nil {
		t.Errorf("Default() error = %v", err)
	}
	if logger == nil {
		t.Error("Default() returned nil")
	}
}

func TestManager_ConcurrentAccess(t *testing.T) {
	cfg := LoggingConfig{
		Default: "console",
		Channels: map[string]ChannelConfig{
			"console": {
				Driver: "console",
				Level:  "debug",
			},
			"file": {
				Driver: "file",
				Level:  "info",
				Path:   "/tmp/test-logs",
			},
		},
	}

	manager := NewManager(cfg)
	done := make(chan bool)

	// Launch multiple goroutines to access channels concurrently
	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 10; j++ {
				channel := "console"
				if j%2 == 0 {
					channel = "file"
				}
				logger, err := manager.Channel(channel)
				if err != nil {
					t.Errorf("Goroutine %d: Channel(%s) error = %v", id, channel, err)
				}
				if logger != nil {
					logger.Info("test", "goroutine", id, "iteration", j)
				}
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}
}

// TestManager_ChannelConcurrentSingleInstance pins the double-build fix:
// the lock is released while createLogger runs (to avoid a stack-driver
// deadlock), so concurrent callers can both build the same channel. The
// loser must be discarded and every caller must observe the SAME logger
// instance, otherwise the loser's resources (e.g. FileLogger descriptors)
// leak. Run under -race to also catch unsynchronised map access.
func TestManager_ChannelConcurrentSingleInstance(t *testing.T) {
	cfg := LoggingConfig{
		Default: "file",
		Channels: map[string]ChannelConfig{
			"x": {
				Driver: "file",
				Path:   t.TempDir(),
			},
		},
	}
	manager := NewManager(cfg)

	const n = 32
	results := make([]Logger, n)
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	done.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer done.Done()
			start.Wait() // line up so all callers race the build
			logger, err := manager.Channel("x")
			if err != nil {
				t.Errorf("goroutine %d: Channel(x) error = %v", idx, err)
				return
			}
			results[idx] = logger
		}(i)
	}
	start.Done()
	done.Wait()

	first := results[0]
	if first == nil {
		t.Fatal("Channel(x) returned nil")
	}
	for i, got := range results {
		if got != first {
			t.Errorf("goroutine %d got a different logger instance %p, want %p", i, got, first)
		}
	}
}

// countingLogger is a test-only Logger that records how many times it is
// asked to shut down. Its construction is counted by the registered factory
// in TestManager_ChannelConcurrentLoserShutdown.
type countingLogger struct {
	shutdowns *int32
}

func (c *countingLogger) Debug(msg string, kvs ...any) {}
func (c *countingLogger) Info(msg string, kvs ...any)  {}
func (c *countingLogger) Warn(msg string, kvs ...any)  {}
func (c *countingLogger) Error(msg string, kvs ...any) {}
func (c *countingLogger) Fatal(msg string, kvs ...any) {}
func (c *countingLogger) Shutdown(ctx context.Context) error {
	atomic.AddInt32(c.shutdowns, 1)
	return nil
}

// TestManager_ChannelConcurrentLoserShutdown proves the stronger contract
// that the single-instance test cannot: when concurrent callers both build
// the same channel, the Manager not only returns one shared instance but
// also Shutdowns every losing duplicate it discards. A registered counting
// driver records constructions and shutdowns; a release barrier forces at
// least two builders to overlap so there is a genuine loser. Removing the
// duplicate Shutdown call in Manager.Channel would drop the shutdown count
// below constructed-1 and fail this test.
func TestManager_ChannelConcurrentLoserShutdown(t *testing.T) {
	var constructed, shutdowns, started int32
	release := make(chan struct{})
	var once sync.Once

	const driverName = "counting-loser-test"
	Drivers().Register(driverName, func(_ context.Context, _ LogConfig) (Logger, error) {
		atomic.AddInt32(&constructed, 1)
		// Block until at least two builders overlap, guaranteeing a loser.
		if atomic.AddInt32(&started, 1) >= 2 {
			once.Do(func() { close(release) })
		}
		<-release
		return &countingLogger{shutdowns: &shutdowns}, nil
	})
	// Safety valve: never hang the suite if overlap somehow fails to occur.
	go func() {
		time.Sleep(2 * time.Second)
		once.Do(func() { close(release) })
	}()

	cfg := LoggingConfig{
		Default: "x",
		Channels: map[string]ChannelConfig{
			"x": {Driver: driverName},
		},
	}
	manager := NewManager(cfg)

	const n = 32
	results := make([]Logger, n)
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	done.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer done.Done()
			start.Wait()
			logger, err := manager.Channel("x")
			if err != nil {
				t.Errorf("goroutine %d: Channel(x) error = %v", idx, err)
				return
			}
			results[idx] = logger
		}(i)
	}
	start.Done()
	done.Wait()

	first := results[0]
	if first == nil {
		t.Fatal("Channel(x) returned nil")
	}
	for i, got := range results {
		if got != first {
			t.Errorf("goroutine %d got a different logger instance %p, want %p", i, got, first)
		}
	}

	built := atomic.LoadInt32(&constructed)
	if built < 2 {
		t.Fatalf("expected concurrent construction overlap (>=2 builds), got %d", built)
	}
	// Every constructed logger except the single stored winner must have
	// been shut down.
	if sd := atomic.LoadInt32(&shutdowns); sd != built-1 {
		t.Errorf("Shutdown attempted %d times, want %d (one per discarded loser)", sd, built-1)
	}
}

// TestManager_ChannelConcurrentStackLoserNonDestructiveShutdown pins the
// stack-channel loser contract: when concurrent callers both build the same
// "stack" channel, the losing duplicate stack still gets a Shutdown attempt,
// but because a manager-built stack does not own its (shared, manager-owned)
// children, that Shutdown must NOT close the winning child out from under the
// stored stack. A counting child driver records constructions and shutdowns;
// the only legitimate child shutdowns come from child-level dedup losers
// (constructed-1). If a discarded stack cascaded Shutdown onto its shared
// child, the count would exceed that and fail this test.
func TestManager_ChannelConcurrentStackLoserNonDestructiveShutdown(t *testing.T) {
	var childConstructed, childShutdowns, started int32
	release := make(chan struct{})
	var once sync.Once

	const childDriver = "counting-stack-loser-child"
	Drivers().Register(childDriver, func(_ context.Context, _ LogConfig) (Logger, error) {
		atomic.AddInt32(&childConstructed, 1)
		// Block until at least two builders overlap. Both stack builders
		// resolve this child concurrently, which both guarantees a child-level
		// race and keeps both stack builders in flight so a stack loser exists.
		if atomic.AddInt32(&started, 1) >= 2 {
			once.Do(func() { close(release) })
		}
		<-release
		return &countingLogger{shutdowns: &childShutdowns}, nil
	})
	go func() {
		time.Sleep(2 * time.Second)
		once.Do(func() { close(release) })
	}()

	cfg := LoggingConfig{
		Default: "stack",
		Channels: map[string]ChannelConfig{
			"child": {Driver: childDriver},
			"stack": {
				Driver:  "stack",
				Options: map[string]any{"channels": []string{"child"}},
			},
		},
	}
	manager := NewManager(cfg)

	const n = 32
	results := make([]Logger, n)
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	done.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer done.Done()
			start.Wait()
			logger, err := manager.Channel("stack")
			if err != nil {
				t.Errorf("goroutine %d: Channel(stack) error = %v", idx, err)
				return
			}
			results[idx] = logger
		}(i)
	}
	start.Done()
	done.Wait()

	first := results[0]
	if first == nil {
		t.Fatal("Channel(stack) returned nil")
	}
	for i, got := range results {
		if got != first {
			t.Errorf("goroutine %d got a different stack instance %p, want %p", i, got, first)
		}
	}

	built := atomic.LoadInt32(&childConstructed)
	if built < 2 {
		t.Fatalf("expected concurrent child construction overlap (>=2 builds), got %d", built)
	}
	// Only child-level dedup losers may be shut down. A discarded stack must
	// not cascade Shutdown onto the shared winning child, so the count stays
	// at exactly constructed-1.
	if sd := atomic.LoadInt32(&childShutdowns); sd != built-1 {
		t.Errorf("child Shutdown attempted %d times, want %d (only child-level dedup losers; a discarded stack must not close shared children)", sd, built-1)
	}
}

// TestManager_StackDriverChannelsAnySlice verifies the Manager accepts a
// []any channel list (the shape JSON/env decoding produces) for the stack
// driver, not only a native []string.
func TestManager_StackDriverChannelsAnySlice(t *testing.T) {
	cfg := LoggingConfig{
		Default: "stack",
		Channels: map[string]ChannelConfig{
			"console": {Driver: "console"},
			"stack": {
				Driver: "stack",
				Options: map[string]any{
					"channels": []any{"console"},
				},
			},
		},
	}
	manager := NewManager(cfg)

	if _, err := manager.Channel("console"); err != nil {
		t.Fatalf("Channel(console) error = %v", err)
	}
	stack, err := manager.Channel("stack")
	if err != nil {
		t.Fatalf("Channel(stack) with []any channels error = %v", err)
	}
	if stack == nil {
		t.Fatal("Channel(stack) returned nil")
	}
}

// TestManager_StackDriverChannelsMalformed verifies a malformed channel
// list (a slice with a non-string element) is a loud error rather than a
// silent misconfiguration.
func TestManager_StackDriverChannelsMalformed(t *testing.T) {
	cfg := LoggingConfig{
		Default: "stack",
		Channels: map[string]ChannelConfig{
			"stack": {
				Driver: "stack",
				Options: map[string]any{
					"channels": []any{"console", 42},
				},
			},
		},
	}
	manager := NewManager(cfg)

	if _, err := manager.Channel("stack"); err == nil {
		t.Error("Channel(stack) with a non-string channel entry should error")
	}
}

func TestStackLogger(t *testing.T) {
	// Create mock loggers
	logger1 := NewNullLogger()
	logger2 := NewNullLogger()

	stack := NewStackLogger(logger1, logger2)

	// These should not panic
	stack.Debug("debug message")
	stack.Info("info message")
	stack.Warn("warn message")
	stack.Error("error message")
	stack.Fatal("fatal message")
}

func TestNullLogger(t *testing.T) {
	logger := NewNullLogger()

	// Test that all methods exist and don't panic
	// Coverage will show these are called
	logger.Debug("debug message", "key", "value")
	logger.Info("info message", "key", "value")
	logger.Warn("warn message", "key", "value")
	logger.Error("error message", "key", "value")
	logger.Fatal("fatal message", "key", "value")
}

func TestManager_StackDriverWithChannels(t *testing.T) {
	cfg := LoggingConfig{
		Default: "stack",
		Channels: map[string]ChannelConfig{
			"console": {
				Driver: "console",
			},
			"file": {
				Driver: "file",
				Path:   "/tmp/test-logs",
			},
			"stack": {
				Driver: "stack",
				Options: map[string]any{
					"channels": []string{"console", "file"},
				},
			},
		},
	}

	manager := NewManager(cfg)

	// First create the dependent channels to avoid deadlock
	console, _ := manager.Channel("console")
	file, _ := manager.Channel("file")

	if console == nil || file == nil {
		t.Fatal("Failed to create dependent channels")
	}

	stack, err := manager.Channel("stack")
	if err != nil {
		t.Errorf("Channel(stack) error = %v", err)
	}
	if stack == nil {
		t.Error("Channel(stack) returned nil")
	}
}

func TestManager_StackDriverWithoutChannels(t *testing.T) {
	cfg := LoggingConfig{
		Default: "stack",
		Channels: map[string]ChannelConfig{
			"stack": {
				Driver: "stack",
			},
		},
	}

	manager := NewManager(cfg)

	_, err := manager.Channel("stack")
	if err == nil {
		t.Error("Channel(stack) without channels option should return error")
	}
}

func TestManager_StackDriverEmptyChannels(t *testing.T) {
	cfg := LoggingConfig{
		Default: "stack",
		Channels: map[string]ChannelConfig{
			"stack": {
				Driver: "stack",
				Options: map[string]any{
					"channels": []string{},
				},
			},
		},
	}

	manager := NewManager(cfg)

	_, err := manager.Channel("stack")
	if err == nil {
		t.Error("Channel(stack) with empty channels should return error")
	}
}

func TestManager_StackDriverInvalidChannel(t *testing.T) {
	cfg := LoggingConfig{
		Default: "stack",
		Channels: map[string]ChannelConfig{
			"stack": {
				Driver: "stack",
				Options: map[string]any{
					"channels": []string{"nonexistent"},
				},
			},
		},
	}

	manager := NewManager(cfg)

	_, err := manager.Channel("stack")
	if err == nil {
		t.Error("Channel(stack) with only invalid channels should return error")
	}
}

// TestManager_StackDriverPartialInvalidChildErrors pins the contract that
// a stack channel surfaces ANY child-resolve failure, not just the case
// where every child fails. A typo in a child name silently degraded the
// stack before; aggregation via errors.Join makes config errors loud at
// boot.
func TestManager_StackDriverPartialInvalidChildErrors(t *testing.T) {
	cfg := LoggingConfig{
		Default: "stack",
		Channels: map[string]ChannelConfig{
			"console": {
				Driver: "console",
			},
			"stack": {
				Driver: "stack",
				Options: map[string]any{
					"channels": []string{"console", "typo"},
				},
			},
		},
	}

	manager := NewManager(cfg)

	// Pre-create the valid child so its absence cannot be the failure cause.
	if _, err := manager.Channel("console"); err != nil {
		t.Fatalf("Channel(console) error = %v", err)
	}

	_, err := manager.Channel("stack")
	if err == nil {
		t.Fatal("Channel(stack) with one invalid child must error, not degrade silently")
	}
	if !strings.Contains(err.Error(), "typo") {
		t.Errorf("error %q must name the failing child", err)
	}
}

func TestManager_FileDriverDefaultPath(t *testing.T) {
	cfg := LoggingConfig{
		Default: "file",
		Channels: map[string]ChannelConfig{
			"file": {
				Driver: "file",
				// No path specified, should use default
			},
		},
	}

	manager := NewManager(cfg)

	file, err := manager.Channel("file")
	if err != nil {
		t.Errorf("Channel(file) error = %v", err)
	}
	if file == nil {
		t.Error("Channel(file) returned nil")
	}
}

func TestManager_UnsupportedDriver(t *testing.T) {
	cfg := LoggingConfig{
		Default: "unsupported",
		Channels: map[string]ChannelConfig{
			"unsupported": {
				Driver: "unsupported",
			},
		},
	}

	manager := NewManager(cfg)

	_, err := manager.Channel("unsupported")
	if err == nil {
		t.Error("Channel(unsupported) should return error for unsupported driver")
	}
}

func TestManager_RaceConditionCoverage(t *testing.T) {
	cfg := LoggingConfig{
		Default: "console",
		Channels: map[string]ChannelConfig{
			"test": {
				Driver: "console",
			},
		},
	}

	manager := NewManager(cfg)

	// Pre-create the channel
	first, _ := manager.Channel("test")

	// Try to get it again - this will hit the double-check branch
	second, _ := manager.Channel("test")

	if first != second {
		t.Error("Should return same logger instance")
	}
}
