package drivers

import (
	"fmt"
	"time"

	"github.com/velocitykode/velocity/log/internal/sanitize"
)

// ConsoleLogger writes log messages to standard output with timestamps.
type ConsoleLogger struct {
	level int // minimum level: 0=debug, 1=info, 2=warn, 3=error, 4=fatal
}

// NewConsoleLogger creates a new console logger that outputs to stdout.
// level sets the minimum severity (0=debug .. 4=fatal).
func NewConsoleLogger(level int) *ConsoleLogger {
	return &ConsoleLogger{level: level}
}

// formatMessage creates a formatted log line with timestamp, level, and key-value pairs.
//
// Every interpolated value (msg, kv keys, kv values) is run through
// sanitize.Value before concatenation. Without this, an attacker who
// controls any field of the record (URL path, user-agent, request
// header echoed into an error message) can drop literal CRLF or ESC
// bytes into the line and forge additional records or drive ANSI
// terminal control sequences when an operator tails the output. See
// log/internal/sanitize and audit finding H-30.
func (c *ConsoleLogger) formatMessage(level, msg string, kvs ...any) string {
	timestamp := time.Now().Format("15:04:05")

	logLine := fmt.Sprintf("[%s] %s: %s", timestamp, level, sanitize.Value(msg))

	if len(kvs) > 0 {
		logLine += " |"
		for i := 0; i < len(kvs); i += 2 {
			if i+1 < len(kvs) {
				// Sanitise both halves: a user-tainted kv key forges
				// a log line just as effectively as a tainted value.
				k := sanitize.Value(fmt.Sprintf("%v", kvs[i]))
				v := sanitize.Value(fmt.Sprintf("%v", kvs[i+1]))
				logLine += fmt.Sprintf(" %s=%s", k, v)
			}
		}
	}

	return logLine
}

// Level returns the configured minimum severity (0=debug .. 4=fatal). A
// redacting wrapper reads this to skip redaction work for records this
// logger would discard by level.
func (c *ConsoleLogger) Level() int { return c.level }

// Debug logs a debug-level message to console
func (c *ConsoleLogger) Debug(msg string, kvs ...any) {
	if c.level > 0 {
		return
	}
	fmt.Println(c.formatMessage("DEBUG", msg, kvs...))
}

// Info logs an info-level message to console
func (c *ConsoleLogger) Info(msg string, kvs ...any) {
	if c.level > 1 {
		return
	}
	fmt.Println(c.formatMessage("INFO", msg, kvs...))
}

// Warn logs a warning-level message to console
func (c *ConsoleLogger) Warn(msg string, kvs ...any) {
	if c.level > 2 {
		return
	}
	fmt.Println(c.formatMessage("WARN", msg, kvs...))
}

// Error logs an error-level message to console
func (c *ConsoleLogger) Error(msg string, kvs ...any) {
	if c.level > 3 {
		return
	}
	fmt.Println(c.formatMessage("ERROR", msg, kvs...))
}

// Fatal logs a fatal-level message to console
func (c *ConsoleLogger) Fatal(msg string, kvs ...any) {
	fmt.Println(c.formatMessage("FATAL", msg, kvs...))
}
