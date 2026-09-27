package log

import (
	"context"
	"strings"

	"github.com/velocitykode/velocity/contract"
)

// Shutdowner is an optional interface loggers may implement for graceful shutdown.
type Shutdowner interface {
	Shutdown(ctx context.Context) error
}

// Logger is the interface every log implementation satisfies: the
// framework's logging contract, contract.Logger, itself.
//
// Implementations must pass logtest.RunLoggerContractTests. See logtest
// for the executable specification.
type Logger = contract.Logger

// NewLogger creates a new Logger instance with the given configuration.
// This is the preferred way to create loggers instead of using the global Init().
//
// The driver name is resolved through the canonical driver registry, so
// third-party drivers registered via Drivers().Register are available
// alongside the built-in console / file / daily / stack / null drivers.
// An empty config.Driver defaults to "console"; if the console driver is
// not registered the registry still returns a clear unknown-driver error.
func NewLogger(config LogConfig) (Logger, error) {
	return NewLoggerWithContext(context.Background(), config)
}

// NewLoggerWithContext is the context-aware variant of NewLogger. The
// ctx is forwarded to the driver factory; the stack driver propagates it
// when resolving its child channels.
//
// An empty config.Driver defaults to "console" (the always-registered
// light root driver) so a zero-value LogConfig produces a working logger
// rather than an unknown-driver error.
func NewLoggerWithContext(ctx context.Context, config LogConfig) (Logger, error) {
	driver := config.Driver
	if driver == "" {
		driver = "console"
		config.Driver = driver
	}
	return driverRegistry.Resolve(ctx, driver, config)
}

// parseLevel converts a level name to its contract.LogLevel, defaulting to
// contract.LogLevelDebug for an unrecognised name.
func parseLevel(s string) contract.LogLevel {
	switch strings.ToLower(s) {
	case "info":
		return contract.LogLevelInfo
	case "warn", "warning":
		return contract.LogLevelWarn
	case "error":
		return contract.LogLevelError
	case "fatal":
		return contract.LogLevelFatal
	default:
		return contract.LogLevelDebug
	}
}
