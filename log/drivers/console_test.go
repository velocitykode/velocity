package drivers

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestNewConsoleLogger(t *testing.T) {
	logger := NewConsoleLogger(0)
	if logger == nil {
		t.Error("NewConsoleLogger(0) returned nil")
	}
}

func TestConsoleLogger_formatMessage(t *testing.T) {
	logger := NewConsoleLogger(0)

	tests := []struct {
		name     string
		level    string
		msg      string
		kvs      []any
		contains []string
	}{
		{
			name:     "simple message",
			level:    "INFO",
			msg:      "test message",
			kvs:      []any{},
			contains: []string{"INFO:", "test message"},
		},
		{
			name:     "message with key-value pairs",
			level:    "ERROR",
			msg:      "error occurred",
			kvs:      []any{"user_id", 123, "action", "login"},
			contains: []string{"ERROR:", "error occurred", "user_id=123", "action=login"},
		},
		{
			name:     "message with odd number of kvs",
			level:    "WARN",
			msg:      "warning",
			kvs:      []any{"key1", "value1", "orphan"},
			contains: []string{"WARN:", "warning", "key1=value1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := logger.formatMessage(tt.level, tt.msg, tt.kvs...)
			for _, expected := range tt.contains {
				if !strings.Contains(result, expected) {
					t.Errorf("formatMessage() missing expected content: %s\nGot: %s", expected, result)
				}
			}
		})
	}
}

func TestConsoleLogger_LogMethods(t *testing.T) {
	logger := NewConsoleLogger(0)

	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Write logs
	logger.Debug("debug msg", "k1", "v1")
	logger.Info("info msg", "k2", "v2")
	logger.Warn("warn msg", "k3", "v3")
	logger.Error("error msg", "k4", "v4")
	logger.Fatal("fatal msg", "k5", "v5")

	// Restore stdout
	w.Close()
	os.Stdout = old

	// Read captured output
	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	// Check all log levels are present
	expectedLevels := []string{"DEBUG:", "INFO:", "WARN:", "ERROR:", "FATAL:"}
	for _, level := range expectedLevels {
		if !strings.Contains(output, level) {
			t.Errorf("Output missing log level: %s", level)
		}
	}

	// Check messages are present
	expectedMessages := []string{"debug msg", "info msg", "warn msg", "error msg", "fatal msg"}
	for _, msg := range expectedMessages {
		if !strings.Contains(output, msg) {
			t.Errorf("Output missing message: %s", msg)
		}
	}

	// Check key-value pairs
	expectedKVs := []string{"k1=v1", "k2=v2", "k3=v3", "k4=v4", "k5=v5"}
	for _, kv := range expectedKVs {
		if !strings.Contains(output, kv) {
			t.Errorf("Output missing key-value pair: %s", kv)
		}
	}
}

func TestConsoleLogger_ConcurrentWrites(t *testing.T) {
	logger := NewConsoleLogger(0)

	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	done := make(chan bool)

	// Launch multiple goroutines to write concurrently
	for i := 0; i < 5; i++ {
		go func(id int) {
			for j := 0; j < 5; j++ {
				logger.Info("concurrent", "goroutine", id, "iteration", j)
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 5; i++ {
		<-done
	}

	// Restore stdout
	w.Close()
	os.Stdout = old

	// Read captured output
	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	// Count lines (should be 25)
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 25 {
		t.Errorf("Expected 25 log lines, got %d", len(lines))
	}

	// Verify all lines have proper format
	for _, line := range lines {
		if !strings.Contains(line, "INFO:") || !strings.Contains(line, "concurrent") {
			t.Errorf("Invalid log line format: %s", line)
		}
	}
}

func TestConsoleLogger_LevelFiltering(t *testing.T) {
	// Level 1 = info: should suppress debug
	logger := NewConsoleLogger(1)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	logger.Debug("should not appear")
	logger.Info("should appear")
	logger.Warn("should appear")

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	if strings.Contains(output, "should not appear") {
		t.Error("Debug message should be filtered at info level")
	}
	if !strings.Contains(output, "INFO:") {
		t.Error("Info message should appear at info level")
	}
	if !strings.Contains(output, "WARN:") {
		t.Error("Warn message should appear at info level")
	}
}
