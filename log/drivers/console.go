package drivers

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/log/internal/sanitize"
)

// ConsoleLogger writes log messages to standard output with timestamps.
type ConsoleLogger struct {
	level contract.LogLevel // lowest level written; Unset writes every level
	out   io.Writer
	// fields are the key-value pairs With bound, written before each
	// line's own pairs.
	fields []any
}

// NewConsoleLogger creates a new console logger that outputs to stdout.
// level sets the lowest severity written.
func NewConsoleLogger(level contract.LogLevel) *ConsoleLogger {
	return &ConsoleLogger{level: level}
}

// NewConsoleLoggerTo creates a console logger that writes to w instead of
// stdout. Used when stdout must stay machine-readable (vel routes --json).
func NewConsoleLoggerTo(w io.Writer, level contract.LogLevel) *ConsoleLogger {
	return &ConsoleLogger{level: level, out: w}
}

// writer returns the configured destination, defaulting to the current
// os.Stdout when none was injected.
func (c *ConsoleLogger) writer() io.Writer {
	if c.out != nil {
		return c.out
	}
	return os.Stdout
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

	if len(c.fields) > 0 || len(kvs) > 0 {
		logLine += " |"
		logLine = appendPairs(logLine, c.fields)
		logLine = appendPairs(logLine, kvs)
	}

	return logLine
}

// appendPairs appends each complete key-value pair of kvs to line as
// " key=value"; a trailing key without a value is left out.
func appendPairs(line string, kvs []any) string {
	for i := 0; i+1 < len(kvs); i += 2 {
		// Sanitise both halves: a user-tainted kv key forges
		// a log line just as effectively as a tainted value.
		k := sanitize.Value(errchain.Sprint(kvs[i]))
		v := sanitize.Value(errchain.Sprint(kvs[i+1]))
		line += " " + k + "=" + v
	}
	return line
}

// With returns a ConsoleLogger writing to the same destination at the same
// level with kvs written before each line's own pairs, after any pairs c
// already binds. A trailing key without a value is left out.
func (c *ConsoleLogger) With(kvs ...any) contract.Logger {
	if len(kvs)%2 == 1 {
		kvs = kvs[:len(kvs)-1]
	}
	fields := make([]any, 0, len(c.fields)+len(kvs))
	fields = append(fields, c.fields...)
	return &ConsoleLogger{level: c.level, out: c.out, fields: append(fields, kvs...)}
}

// Level returns the configured minimum severity. A redacting wrapper reads
// this to skip redaction work for records this logger would discard by
// level.
func (c *ConsoleLogger) Level() contract.LogLevel { return c.level }

// Debug logs a debug-level message to console
func (c *ConsoleLogger) Debug(msg string, kvs ...any) {
	if c.level > contract.LogLevelDebug {
		return
	}
	fmt.Fprintln(c.writer(), c.formatMessage("DEBUG", msg, kvs...))
}

// Info logs an info-level message to console
func (c *ConsoleLogger) Info(msg string, kvs ...any) {
	if c.level > contract.LogLevelInfo {
		return
	}
	fmt.Fprintln(c.writer(), c.formatMessage("INFO", msg, kvs...))
}

// Warn logs a warning-level message to console
func (c *ConsoleLogger) Warn(msg string, kvs ...any) {
	if c.level > contract.LogLevelWarn {
		return
	}
	fmt.Fprintln(c.writer(), c.formatMessage("WARN", msg, kvs...))
}

// Error logs an error-level message to console
func (c *ConsoleLogger) Error(msg string, kvs ...any) {
	if c.level > contract.LogLevelError {
		return
	}
	fmt.Fprintln(c.writer(), c.formatMessage("ERROR", msg, kvs...))
}

// Fatal logs a fatal-level message to console
func (c *ConsoleLogger) Fatal(msg string, kvs ...any) {
	fmt.Fprintln(c.writer(), c.formatMessage("FATAL", msg, kvs...))
}
