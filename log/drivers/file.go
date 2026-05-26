package drivers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/velocitykode/velocity/log/internal/sanitize"
)

// FileLogger writes log messages to daily rotating files.
// Thread-safe with automatic date-based file rotation and optional retention cleanup.
type FileLogger struct {
	path  string
	days  int // retention days; 0 means keep forever
	level int // minimum level: 0=debug, 1=info, 2=warn, 3=error, 4=fatal
	mu    sync.Mutex
	file  *os.File
	date  string
}

// NewFileLogger creates a file logger that writes to the specified directory.
// Log files are named velocity-YYYY-MM-DD.log and rotate daily.
// days sets retention (0 = keep forever). level sets minimum severity (0=debug .. 4=fatal).
func NewFileLogger(path string, days int, level int) *FileLogger {
	return &FileLogger{
		path:  path,
		days:  days,
		level: level,
	}
}

// ensureFile ensures the log file exists and handles daily rotation.
// Creates new file if date has changed or file doesn't exist
func (f *FileLogger) ensureFile() error {
	currentDate := time.Now().Format("2006-01-02")

	if f.date == currentDate && f.file != nil {
		return nil
	}

	if f.file != nil {
		err := f.file.Close()
		if err != nil {
			return err
		}
	}

	if err := os.MkdirAll(f.path, 0755); err != nil {
		return err
	}

	filename := filepath.Join(f.path, fmt.Sprintf("velocity-%s.log", currentDate))
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}

	f.file = file
	oldDate := f.date
	f.date = currentDate

	// Clean up old log files on rotation (date changed)
	if f.days > 0 && oldDate != currentDate {
		go f.cleanup()
	}

	return nil
}

// log writes a formatted message to the log file with proper locking
func (f *FileLogger) log(level, msg string, kvs ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.ensureFile(); err != nil {
		fmt.Printf("Failed to open log file: %v\n", err)
		return
	}

	timestamp := time.Now().Format("2006-01-02 15:04:05.000")

	// Sanitise the user-controlled msg before emit. Without this, a
	// caller passing an HTTP URL path or any other request-derived
	// string can drop a literal CRLF into the log file and forge a
	// second record. See log/internal/sanitize and audit finding H-30.
	logLine := fmt.Sprintf("[%s] %s: %s", timestamp, level, sanitize.Value(msg))

	if len(kvs) > 0 {
		logLine += " |"
		for i := 0; i < len(kvs); i += 2 {
			if i+1 < len(kvs) {
				// Both halves of the kv pair are sanitised: nothing in
				// the framework prevents a user-tainted string from
				// being passed as a key, and a CRLF in the key forges
				// a log line just as effectively as one in the value.
				k := sanitize.Value(fmt.Sprintf("%v", kvs[i]))
				v := sanitize.Value(fmt.Sprintf("%v", kvs[i+1]))
				logLine += fmt.Sprintf(" %s=%s", k, v)
			}
		}
	}

	_, err := fmt.Fprintln(f.file, logLine)
	if err != nil {
		return
	}
}

// Debug logs a debug-level message to file
func (f *FileLogger) Debug(msg string, kvs ...any) {
	if f.level > 0 {
		return
	}
	f.log("DEBUG", msg, kvs...)
}

// Info logs an info-level message to file
func (f *FileLogger) Info(msg string, kvs ...any) {
	if f.level > 1 {
		return
	}
	f.log("INFO", msg, kvs...)
}

// Warn logs a warning-level message to file
func (f *FileLogger) Warn(msg string, kvs ...any) {
	if f.level > 2 {
		return
	}
	f.log("WARN", msg, kvs...)
}

// Error logs an error-level message to file
func (f *FileLogger) Error(msg string, kvs ...any) {
	if f.level > 3 {
		return
	}
	f.log("ERROR", msg, kvs...)
}

// Fatal logs a fatal-level message to file
func (f *FileLogger) Fatal(msg string, kvs ...any) {
	f.log("FATAL", msg, kvs...)
}

// cleanup removes log files older than the configured retention period.
func (f *FileLogger) cleanup() {
	if f.days <= 0 {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -f.days)
	entries, err := os.ReadDir(f.path)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if len(name) < 23 || name[:9] != "velocity-" || name[len(name)-4:] != ".log" {
			continue
		}
		dateStr := name[9 : len(name)-4] // extract YYYY-MM-DD
		fileDate, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			continue
		}
		if fileDate.Before(cutoff) {
			os.Remove(filepath.Join(f.path, name))
		}
	}
}

// Shutdown closes the underlying file handle, honoring the context deadline.
func (f *FileLogger) Shutdown(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file != nil {
		err := f.file.Close()
		f.file = nil
		return err
	}
	return nil
}
