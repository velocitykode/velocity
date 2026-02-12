package log

import (
	"testing"
)

func TestNewLogger_ConsoleDriver(t *testing.T) {
	logger, err := NewLogger(LogConfig{
		Driver: "console",
		Config: map[string]any{},
	})
	if err != nil {
		t.Fatalf("NewLogger(console) error = %v", err)
	}
	if logger == nil {
		t.Fatal("NewLogger(console) returned nil")
	}
}

func TestNewLogger_FileDriver(t *testing.T) {
	tests := []struct {
		name   string
		config LogConfig
	}{
		{
			name: "file driver with path",
			config: LogConfig{
				Driver: "file",
				Config: map[string]any{"path": "/tmp/test-logs"},
			},
		},
		{
			name: "file driver without path",
			config: LogConfig{
				Driver: "file",
				Config: map[string]any{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, err := NewLogger(tt.config)
			if err != nil {
				t.Fatalf("NewLogger() error = %v", err)
			}
			if logger == nil {
				t.Fatal("NewLogger() returned nil")
			}
		})
	}
}

func TestNewLogger_InvalidDriver(t *testing.T) {
	_, err := NewLogger(LogConfig{Driver: "invalid_driver_that_will_fail"})
	if err == nil {
		t.Error("Expected error for invalid driver")
	}
}

func TestNewLogger_UnsupportedDriver(t *testing.T) {
	_, err := NewLogger(LogConfig{
		Driver: "unknown",
		Config: map[string]any{},
	})
	if err == nil {
		t.Error("Expected error for unsupported driver")
	}
}

func TestLoggerMethods_DoNotPanic(t *testing.T) {
	logger, err := NewLogger(LogConfig{
		Driver: "console",
		Config: map[string]any{},
	})
	if err != nil {
		t.Fatalf("NewLogger() error = %v", err)
	}

	// These should not panic
	logger.Debug("test debug message", "key", "value")
	logger.Info("test info message", "key", "value")
	logger.Warn("test warn message", "key", "value")
	logger.Error("test error message", "key", "value")
}

func TestLoggerFatal(t *testing.T) {
	// Test that the Logger interface includes Fatal method
	// We use a mock to avoid os.Exit
	called := false
	mock := &mockLogger{
		onFatal: func(msg string, kvs ...any) {
			called = true
		},
	}

	var logger Logger = mock
	logger.Fatal("test", "key", "value")

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
