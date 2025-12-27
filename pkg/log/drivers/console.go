package drivers

import (
	"fmt"
	"time"
)

// ConsoleLogger writes log messages to standard output with timestamps
type ConsoleLogger struct{}

// NewConsoleLogger creates a new console logger that outputs to stdout
func NewConsoleLogger() *ConsoleLogger {
	return &ConsoleLogger{}
}

// formatMessage creates a formatted log line with timestamp, level, and key-value pairs
func (c *ConsoleLogger) formatMessage(level, msg string, kvs ...any) string {
	timestamp := time.Now().Format("15:04:05")

	logLine := fmt.Sprintf("[%s] %s: %s", timestamp, level, msg)

	if len(kvs) > 0 {
		logLine += " |"
		for i := 0; i < len(kvs); i += 2 {
			if i+1 < len(kvs) {
				logLine += fmt.Sprintf(" %v=%v", kvs[i], kvs[i+1])
			}
		}
	}

	return logLine
}

// Debug logs a debug-level message to console
func (c *ConsoleLogger) Debug(msg string, kvs ...any) {
	fmt.Println(c.formatMessage("DEBUG", msg, kvs...))
}

// Info logs an info-level message to console
func (c *ConsoleLogger) Info(msg string, kvs ...any) {
	fmt.Println(c.formatMessage("INFO", msg, kvs...))
}

// Warn logs a warning-level message to console
func (c *ConsoleLogger) Warn(msg string, kvs ...any) {
	fmt.Println(c.formatMessage("WARN", msg, kvs...))
}

// Error logs an error-level message to console
func (c *ConsoleLogger) Error(msg string, kvs ...any) {
	fmt.Println(c.formatMessage("ERROR", msg, kvs...))
}

// Fatal logs a fatal-level message to console
func (c *ConsoleLogger) Fatal(msg string, kvs ...any) {
	fmt.Println(c.formatMessage("FATAL", msg, kvs...))
}
