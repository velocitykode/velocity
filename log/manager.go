package log

import (
	"context"
	"fmt"
	"sync"

	"github.com/velocitykode/velocity/config"
	"github.com/velocitykode/velocity/log/drivers"
)

// Manager handles multiple logger channels for advanced logging scenarios.
// Each channel can have its own driver configuration (file, console, stack, null)
type Manager struct {
	config   config.LoggingConfig
	channels map[string]Logger
	mu       sync.RWMutex
}

// NewManager creates a new logger manager with the given configuration.
// The manager allows different log channels with independent drivers and settings
func NewManager(cfg config.LoggingConfig) *Manager {
	return &Manager{
		config:   cfg,
		channels: make(map[string]Logger),
	}
}

// Channel returns a logger for the specified channel, creating it if needed.
// Thread-safe and uses double-checked locking for performance
func (m *Manager) Channel(name string) (Logger, error) {
	m.mu.RLock()
	if logger, exists := m.channels[name]; exists {
		m.mu.RUnlock()
		return logger, nil
	}
	m.mu.RUnlock()

	// Create the channel
	m.mu.Lock()

	// Double-check after acquiring write lock
	if logger, exists := m.channels[name]; exists {
		m.mu.Unlock()
		return logger, nil
	}

	channelConfig, exists := m.config.Channels[name]
	if !exists {
		m.mu.Unlock()
		return nil, fmt.Errorf("channel %s not configured", name)
	}

	// Release lock before creating logger to avoid deadlock with stack driver
	m.mu.Unlock()

	logger, err := m.createLogger(channelConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger for channel %s: %w", name, err)
	}

	// Re-acquire lock to store the logger
	m.mu.Lock()
	m.channels[name] = logger
	m.mu.Unlock()

	return logger, nil
}

// Default returns the default logger channel as configured in LoggingConfig
func (m *Manager) Default() (Logger, error) {
	return m.Channel(m.config.Default)
}

// createLogger creates a logger instance based on the channel configuration.
// Supports file, console, stack (multi-logger), and null drivers
func (m *Manager) createLogger(cfg config.ChannelConfig) (Logger, error) {
	level := parseLevel(cfg.Level)

	switch cfg.Driver {
	case "file", "daily":
		path := cfg.Path
		if path == "" {
			path = "./storage/logs"
		}
		days := 14
		if cfg.MaxAge > 0 {
			days = cfg.MaxAge
		}
		return drivers.NewFileLogger(path, days, level), nil

	case "console":
		return drivers.NewConsoleLogger(level), nil

	case "stack":
		// Stack driver logs to multiple channels
		if channelNames, ok := cfg.Options["channels"].([]string); ok {
			var loggers []Logger
			for _, channelName := range channelNames {
				logger, err := m.Channel(channelName)
				if err != nil {
					continue // Skip failed channels
				}
				loggers = append(loggers, logger)
			}
			if len(loggers) > 0 {
				return NewStackLogger(loggers...), nil
			}
		}
		return nil, fmt.Errorf("stack driver requires channels option")

	case "null":
		return NewNullLogger(), nil

	default:
		return nil, fmt.Errorf("unsupported logger driver: %s", cfg.Driver)
	}
}

// StackLogger logs to multiple loggers simultaneously.
// Useful for logging to multiple destinations (e.g., file and console)
type StackLogger struct {
	loggers []Logger
}

// NewStackLogger creates a logger that writes to multiple loggers.
// All provided loggers will receive the same log messages
func NewStackLogger(loggers ...Logger) *StackLogger {
	return &StackLogger{loggers: loggers}
}

// Debug logs a debug message to all configured loggers
func (s *StackLogger) Debug(msg string, kvs ...any) {
	for _, logger := range s.loggers {
		logger.Debug(msg, kvs...)
	}
}

// Info logs an info message to all configured loggers
func (s *StackLogger) Info(msg string, kvs ...any) {
	for _, logger := range s.loggers {
		logger.Info(msg, kvs...)
	}
}

// Warn logs a warning message to all configured loggers
func (s *StackLogger) Warn(msg string, kvs ...any) {
	for _, logger := range s.loggers {
		logger.Warn(msg, kvs...)
	}
}

// Error logs an error message to all configured loggers
func (s *StackLogger) Error(msg string, kvs ...any) {
	for _, logger := range s.loggers {
		logger.Error(msg, kvs...)
	}
}

// Fatal logs a fatal message to all configured loggers
func (s *StackLogger) Fatal(msg string, kvs ...any) {
	for _, logger := range s.loggers {
		logger.Fatal(msg, kvs...)
	}
}

// Shutdown closes all underlying loggers that support it, honoring the
// context deadline.
func (s *StackLogger) Shutdown(ctx context.Context) error {
	var firstErr error
	for _, l := range s.loggers {
		if shutdowner, ok := l.(Shutdowner); ok {
			if err := shutdowner.Shutdown(ctx); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// NullLogger discards all log messages without any output.
// Useful for testing or disabling logging for specific channels
type NullLogger struct{}

// NewNullLogger creates a logger that silently discards all messages.
// No output is produced regardless of log level
func NewNullLogger() *NullLogger {
	return &NullLogger{}
}

func (n *NullLogger) Debug(msg string, kvs ...any) {}
func (n *NullLogger) Info(msg string, kvs ...any)  {}
func (n *NullLogger) Warn(msg string, kvs ...any)  {}
func (n *NullLogger) Error(msg string, kvs ...any) {}
func (n *NullLogger) Fatal(msg string, kvs ...any) {}
