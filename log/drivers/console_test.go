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

// TestConsoleLogger_SanitisesCRLFInValue replays the H-30 threat for
// the console driver: a value containing CRLF must not produce
// additional newlines in the formatted line. The sanitiser
// substitutes \x0d / \x0a hex escapes so a single log call still
// produces exactly one line.
func TestConsoleLogger_SanitisesCRLFInValue(t *testing.T) {
	logger := NewConsoleLogger(0)
	line := logger.formatMessage("INFO", "not found",
		"url", "/vulnpath\r\n[2026-01-01] FATAL: Database deleted")

	if strings.Contains(line, "\r") || strings.Contains(line, "\n") {
		t.Errorf("console formatMessage preserved literal CRLF (log forgery):\n%q", line)
	}
	if !strings.Contains(line, `\x0d\x0a`) {
		t.Errorf("console formatMessage missing CRLF escape \\x0d\\x0a:\n%s", line)
	}
	if !strings.Contains(line, "[2026-01-01] FATAL: Database deleted") {
		t.Errorf("console formatMessage dropped sanitised content:\n%s", line)
	}
}

// TestConsoleLogger_SanitisesANSIEscape verifies ESC (0x1b) is escaped
// so a dev watching stdout cannot have their terminal driven by an
// attacker-controlled URL.
func TestConsoleLogger_SanitisesANSIEscape(t *testing.T) {
	logger := NewConsoleLogger(0)
	line := logger.formatMessage("INFO", "request",
		"url", "/path\x1b[2J")

	if strings.ContainsRune(line, 0x1b) {
		t.Errorf("console formatMessage preserved literal ESC (terminal hijack):\n%q", line)
	}
	if !strings.Contains(line, `\x1b`) {
		t.Errorf("console formatMessage missing ESC escape \\x1b:\n%s", line)
	}
}

// TestConsoleLogger_SanitisesKey: same as the file driver test;
// CRLF in a kv key must be escaped or it forges a log line.
func TestConsoleLogger_SanitisesKey(t *testing.T) {
	logger := NewConsoleLogger(0)
	line := logger.formatMessage("INFO", "test", "tainted\nkey", "value")

	if strings.Contains(line, "\n") {
		t.Errorf("console formatMessage preserved literal LF in key:\n%q", line)
	}
	if !strings.Contains(line, `tainted\x0akey=value`) {
		t.Errorf("console formatMessage did not escape LF in key:\n%s", line)
	}
}

// TestConsoleLogger_SanitisesMessage covers the msg path, where
// err.Error() is typically interpolated.
func TestConsoleLogger_SanitisesMessage(t *testing.T) {
	logger := NewConsoleLogger(0)
	line := logger.formatMessage("ERROR", "decode failed: /api/users\r\n[FORGED] FATAL: db down")

	if strings.Contains(line, "\r") || strings.Contains(line, "\n") {
		t.Errorf("console formatMessage preserved literal CRLF in msg:\n%q", line)
	}
	if !strings.Contains(line, `\x0d\x0a`) {
		t.Errorf("console formatMessage missing CRLF escape in msg:\n%s", line)
	}
}

// TestConsoleLogger_PreservesTab guards the columnar-alignment
// carve-out: TAB (0x09) is the sole sub-0x20 byte that passes
// through, so structured text logs remain readable.
func TestConsoleLogger_PreservesTab(t *testing.T) {
	logger := NewConsoleLogger(0)
	line := logger.formatMessage("INFO", "col1\tcol2", "a", "b\tc")

	if !strings.Contains(line, "col1\tcol2") {
		t.Errorf("console formatMessage stripped TAB from msg:\n%q", line)
	}
	if !strings.Contains(line, "a=b\tc") {
		t.Errorf("console formatMessage stripped TAB from value:\n%q", line)
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
