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

// SetGlobalLogger sets the global logger instance.
// Used by velocity.Default() to wire the App's logger into the global.
func SetGlobalLogger(l Logger) {
	mu.Lock()
	defer mu.Unlock()
	instance = l
}

// NewLogger creates a new Logger instance with the given configuration.
// This is the preferred way to create loggers instead of using the global Init().
func NewLogger(config LogConfig) (Logger, error) {
	return createDriver(config.Driver, config.Config)
}

// createDriver creates a Logger from a driver name and config map.
func createDriver(driver string, config map[string]any) (Logger, error) {
	switch driver {
	case "file":
		path := "./storage/logs"
		if p, ok := config["path"].(string); ok {
			path = p
		}
		return drivers.NewFileLogger(path), nil
	case "console":
		return drivers.NewConsoleLogger(), nil
	default:
		return nil, fmt.Errorf("unsupported logger driver: %s", driver)
	}
}

// Init initializes the global logger with the specified driver and configuration.
// Supported drivers: "console", "file"
// Config options vary by driver - file driver accepts "path" for log directory.
func Init(driver string, config map[string]any) error {
	mu.Lock()
	defer mu.Unlock()

	l, err := createDriver(driver, config)
	if err != nil {
		return err
	}

	instance = l
	return nil
}

// Get returns the global logger instance, creating a default console logger if needed.
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

// Debug logs a debug-level message with optional key-value pairs.
func Debug(msg string, kvs ...any) {
	Get().Debug(msg, kvs...)
}

// Info logs an info-level message with optional key-value pairs.
func Info(msg string, kvs ...any) {
	Get().Info(msg, kvs...)
}

// Warn logs a warning-level message with optional key-value pairs.
func Warn(msg string, kvs ...any) {
	Get().Warn(msg, kvs...)
}

// Error logs an error-level message with optional key-value pairs.
func Error(msg string, kvs ...any) {
	Get().Error(msg, kvs...)
}

// Fatal logs a fatal-level message with optional key-value pairs and exits the program.
func Fatal(msg string, kvs ...any) {
	Get().Fatal(msg, kvs...)
	os.Exit(1)
}
