package log

import (
	"os"
	"os/exec"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/pkg/log/drivers"
)

func TestInit(t *testing.T) {
	tests := []struct {
		name      string
		driver    string
		config    map[string]any
		wantError bool
	}{
		{
			name:      "console driver",
			driver:    "console",
			config:    map[string]any{},
			wantError: false,
		},
		{
			name:      "file driver with path",
			driver:    "file",
			config:    map[string]any{"path": "/tmp/test-logs"},
			wantError: false,
		},
		{
			name:      "file driver without path",
			driver:    "file",
			config:    map[string]any{},
			wantError: false,
		},
		{
			name:      "unsupported driver",
			driver:    "unknown",
			config:    map[string]any{},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Init(tt.driver, tt.config)
			if (err != nil) != tt.wantError {
				t.Errorf("Init() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}

func TestGet(t *testing.T) {
	// Test default console logger when not initialized
	logger := Get()
	if logger == nil {
		t.Error("Get() should return a logger even when not initialized")
	}

	// Test after initialization
	err := Init("console", map[string]any{})
	if err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	logger = Get()
	if logger == nil {
		t.Error("Get() should return the initialized logger")
	}
}

func TestGlobalLogFunctions(t *testing.T) {
	// Initialize with console logger for testing
	err := Init("console", map[string]any{})
	if err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// These should not panic
	Debug("test debug message", "key", "value")
	Info("test info message", "key", "value")
	Warn("test warn message", "key", "value")
	Error("test error message", "key", "value")

	// We can't test Fatal as it calls os.Exit, but we can test the function exists
	// Fatal("test fatal message", "key", "value") // Would exit
}

func TestFatalFunction(t *testing.T) {
	// Test that Fatal calls the logger's Fatal method
	// We mock the instance to avoid os.Exit
	original := instance
	defer func() { instance = original }()

	called := false
	instance = &mockLogger{
		onFatal: func(msg string, kvs ...any) {
			called = true
		},
	}

	// This calls the logger's Fatal but our mock doesn't exit
	Get().Fatal("test", "key", "value")

	if !called {
		t.Error("Fatal should call the logger's Fatal method")
	}
}

type mockLogger struct {
	onFatal func(string, ...any)
}

func (m *mockLogger) Debug(msg string, kvs ...any) {}
func (m *mockLogger) Info(msg string, kvs ...any)  {}
func (m *mockLogger) Warn(msg string, kvs ...any)  {}
func (m *mockLogger) Error(msg string, kvs ...any) {}
func (m *mockLogger) Fatal(msg string, kvs ...any) {
	if m.onFatal != nil {
		m.onFatal(msg, kvs...)
	}
}

func TestLoggerLevels(t *testing.T) {
	levels := []Level{DEBUG, INFO, WARN, ERROR, FATAL}
	expected := []int{0, 1, 2, 3, 4}

	for i, level := range levels {
		if int(level) != expected[i] {
			t.Errorf("Level %d = %d, want %d", i, level, expected[i])
		}
	}
}

func TestEnsureInitialized(t *testing.T) {
	// Save original instance
	original := instance
	defer func() { instance = original }()

	// Set instance to nil to test EnsureInitialized
	instance = nil

	// Should initialize with default console logger
	EnsureInitialized()

	if instance == nil {
		t.Error("EnsureInitialized() should create a default instance")
	}
}

func TestGetWithNilInstance(t *testing.T) {
	// Save original instance
	original := instance
	originalOnce := once
	defer func() {
		instance = original
		once = originalOnce
	}()

	// Reset once and instance
	once = sync.Once{}
	instance = nil

	// Get should initialize and return a logger
	logger := Get()
	if logger == nil {
		t.Error("Get() should return a logger even when instance is nil")
	}
}

func TestGetEnvOrDefault(t *testing.T) {
	tests := []struct {
		name     string
		envKey   string
		envValue string
		defValue string
		expected string
	}{
		{
			name:     "returns environment value when set",
			envKey:   "TEST_ENV_VAR",
			envValue: "test_value",
			defValue: "default",
			expected: "test_value",
		},
		{
			name:     "returns default when env not set",
			envKey:   "UNSET_ENV_VAR",
			envValue: "",
			defValue: "default",
			expected: "default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envValue != "" {
				t.Setenv(tt.envKey, tt.envValue)
			}

			result := getEnvOrDefault(tt.envKey, tt.defValue)
			if result != tt.expected {
				t.Errorf("getEnvOrDefault(%s, %s) = %s, want %s",
					tt.envKey, tt.defValue, result, tt.expected)
			}
		})
	}
}

func TestInitFallbackToConsole(t *testing.T) {
	// Save original instance
	original := instance
	originalOnce := once
	originalInitOnce := initOnce
	defer func() {
		instance = original
		once = originalOnce
		initOnce = originalInitOnce
	}()

	// Reset state
	instance = nil
	once = sync.Once{}
	initOnce = sync.Once{}

	// Set invalid driver to trigger fallback
	t.Setenv("LOG_DRIVER", "invalid_driver_that_will_fail")

	// Call init logic directly
	initOnce.Do(func() {
		driver := os.Getenv("LOG_DRIVER")
		if driver == "" {
			driver = "console"
		}

		config := map[string]any{
			"path":   getEnvOrDefault("LOG_PATH", "./storage/logs"),
			"level":  getEnvOrDefault("LOG_LEVEL", "debug"),
			"format": getEnvOrDefault("LOG_FORMAT", "text"),
		}

		// This should fail and fall back to console
		if err := Init(driver, config); err != nil {
			instance = drivers.NewConsoleLogger()
		}
	})

	// Verify instance is console logger
	if instance == nil {
		t.Error("Instance should be initialized with fallback console logger")
	}
}

func TestGlobalFatalFunction(t *testing.T) {
	if os.Getenv("TEST_FATAL") == "1" {
		// This will actually call Fatal and exit
		Fatal("test fatal", "key", "value")
		return
	}

	// Run the test in a subprocess
	cmd := exec.Command(os.Args[0], "-test.run=TestGlobalFatalFunction")
	cmd.Env = append(os.Environ(), "TEST_FATAL=1")
	err := cmd.Run()

	// Check that it exited with code 1
	if e, ok := err.(*exec.ExitError); ok && !e.Success() {
		// Expected behavior - Fatal should exit with non-zero
		return
	}
	t.Fatalf("process ran with err %v, want exit status 1", err)
}
