package log

import (
	"fmt"
	"os"
	"sync"

	"github.com/velocitykode/velocity/pkg/log/drivers"
)

// Level represents the severity of a log message
type Level int

const (
	DEBUG Level = iota
	INFO
	WARN
	ERROR
	FATAL
)

// Logger defines the interface for all log implementations
type Logger interface {
	Debug(msg string, kvs ...any)
	Info(msg string, kvs ...any)
	Warn(msg string, kvs ...any)
	Error(msg string, kvs ...any)
	Fatal(msg string, kvs ...any)
}

var (
	instance Logger
	once     sync.Once
	mu       sync.RWMutex
)

// Init initializes the global logger with the specified driver and configuration.
// Supported drivers: "console", "file"
// Config options vary by driver - file driver accepts "path" for log directory
func Init(driver string, config map[string]any) error {
	mu.Lock()
	defer mu.Unlock()

	switch driver {
	case "file":
		path := "./storage/logs"
		if p, ok := config["path"].(string); ok {
			path = p
		}
		instance = drivers.NewFileLogger(path)
	case "console":
		instance = drivers.NewConsoleLogger()
	default:
		return fmt.Errorf("unsupported logger driver: %s", driver)
	}

	return nil
}

// Get returns the global logger instance, creating a default console logger if needed
func Get() Logger {
	mu.RLock()
	defer mu.RUnlock()

	if instance == nil {
		once.Do(func() {
			instance = drivers.NewConsoleLogger()
		})
	}
	return instance
}

// Debug logs a debug-level message with optional key-value pairs
func Debug(msg string, kvs ...any) {
	Get().Debug(msg, kvs...)
}

// Info logs an info-level message with optional key-value pairs
func Info(msg string, kvs ...any) {
	Get().Info(msg, kvs...)
}

// Warn logs a warning-level message with optional key-value pairs
func Warn(msg string, kvs ...any) {
	Get().Warn(msg, kvs...)
}

// Error logs an error-level message with optional key-value pairs
func Error(msg string, kvs ...any) {
	Get().Error(msg, kvs...)
}

// Fatal logs a fatal-level message with optional key-value pairs and exits the program
func Fatal(msg string, kvs ...any) {
	Get().Fatal(msg, kvs...)
	os.Exit(1)
}
