package drivers

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FileLogger writes log messages to daily rotating files.
// Thread-safe with automatic date-based file rotation
type FileLogger struct {
	path string
	mu   sync.Mutex
	file *os.File
	date string
}

// NewFileLogger creates a file logger that writes to the specified directory.
// Log files are named velocity-YYYY-MM-DD.log and rotate daily
func NewFileLogger(path string) *FileLogger {
	return &FileLogger{
		path: path,
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
	f.date = currentDate
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

	logLine := fmt.Sprintf("[%s] %s: %s", timestamp, level, msg)

	if len(kvs) > 0 {
		logLine += " |"
		for i := 0; i < len(kvs); i += 2 {
			if i+1 < len(kvs) {
				logLine += fmt.Sprintf(" %v=%v", kvs[i], kvs[i+1])
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
	f.log("DEBUG", msg, kvs...)
}

// Info logs an info-level message to file
func (f *FileLogger) Info(msg string, kvs ...any) {
	f.log("INFO", msg, kvs...)
}

// Warn logs a warning-level message to file
func (f *FileLogger) Warn(msg string, kvs ...any) {
	f.log("WARN", msg, kvs...)
}

// Error logs an error-level message to file
func (f *FileLogger) Error(msg string, kvs ...any) {
	f.log("ERROR", msg, kvs...)
}

// Fatal logs a fatal-level message to file
func (f *FileLogger) Fatal(msg string, kvs ...any) {
	f.log("FATAL", msg, kvs...)
}

// Close closes the underlying file handle.
func (f *FileLogger) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file != nil {
		err := f.file.Close()
		f.file = nil
		return err
	}
	return nil
}
