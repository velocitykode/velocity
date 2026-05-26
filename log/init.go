package log

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/velocitykode/velocity/driverregistry"
	"github.com/velocitykode/velocity/log/drivers"
)

// LogConfig holds configuration for creating a Logger instance.
type LogConfig struct {
	// Driver specifies the log driver: "console", "file", "stack", "null", or
	// any third-party driver registered via Drivers().Register.
	Driver string
	// Config holds driver-specific options (e.g., "path" for file driver,
	// "stack" with []string of channel names for the stack driver, "level"
	// for any driver).
	Config map[string]any
}

// driverRegistry is the canonical Velocity driver registry for loggers.
// Built-in drivers (console, file/daily, stack, null) self-register from
// this file's init(); third-party drivers can register additional
// factories.
var driverRegistry = driverregistry.New[Logger, LogConfig]("log")

// Drivers returns the registry that log driver factories register
// themselves into. Use this from a driver package's init() to install a
// factory:
//
//	func init() {
//	    log.Drivers().Register("syslog", func(_ context.Context, cfg log.LogConfig) (log.Logger, error) {
//	        return newSyslogLogger(cfg.Config), nil
//	    })
//	}
func Drivers() *driverregistry.Registry[Logger, LogConfig] { return driverRegistry }

func init() {
	Drivers().Register("console", func(_ context.Context, cfg LogConfig) (Logger, error) {
		return wrapWithRedactors(drivers.NewConsoleLogger(extractLevel(cfg.Config)), cfg), nil
	})

	fileFactory := func(_ context.Context, cfg LogConfig) (Logger, error) {
		path := "./storage/logs"
		if p, ok := cfg.Config["path"].(string); ok {
			path = p
		}
		days := 14
		if d, ok := cfg.Config["days"].(int); ok {
			days = d
		}
		return wrapWithRedactors(drivers.NewFileLogger(path, days, extractLevel(cfg.Config)), cfg), nil
	}
	Drivers().Register("file", fileFactory)
	Drivers().Register("daily", fileFactory)

	Drivers().Register("null", func(_ context.Context, _ LogConfig) (Logger, error) {
		return NewNullLogger(), nil
	})

	Drivers().Register("stack", func(ctx context.Context, cfg LogConfig) (Logger, error) {
		var channels []string
		if ch, ok := cfg.Config["stack"].([]string); ok {
			channels = ch
		}
		if len(channels) == 0 {
			channels = []string{"console", "daily"}
		}
		// Resolve every requested child; aggregate failures with errors.Join
		// so a typo or missing driver in ANY child takes the whole stack
		// down loudly at boot rather than silently degrading. Continuing
		// with surviving children would mask config errors that must be
		// fixed before the app keeps running.
		var (
			loggers   []Logger
			childErrs []error
		)
		for _, name := range channels {
			if name == "stack" {
				continue // prevent recursion
			}
			child, err := driverRegistry.Resolve(ctx, name, LogConfig{Driver: name, Config: cfg.Config})
			if err != nil {
				childErrs = append(childErrs, fmt.Errorf("velocity/log: stack driver: child %q: %w", name, err))
				continue
			}
			loggers = append(loggers, child)
		}
		if len(childErrs) > 0 {
			return nil, errors.Join(childErrs...)
		}
		if len(loggers) == 0 {
			return nil, fmt.Errorf("velocity/log: stack driver: no valid channels configured")
		}
		return NewStackLogger(loggers...), nil
	})
}

// extractLevel pulls a "level" string from a driver config map and returns
// its numeric value, defaulting to DEBUG when absent or unrecognised.
func extractLevel(config map[string]any) int {
	if config == nil {
		return int(DEBUG)
	}
	if l, ok := config["level"].(string); ok {
		return parseLevel(l)
	}
	return int(DEBUG)
}

// wrapWithRedactors layers the default redactor chain on top of a
// freshly constructed driver Logger when the config opts in. Three
// equivalent opt-ins exist so operators can choose the surface that
// best matches their bootstrap:
//
//   - cfg.Config["redact"] == true: per-channel JSON-friendly toggle.
//   - cfg.Config["redactors"] []Redactor: caller-supplied chain that
//     bypasses BuildDefaultRedactors entirely. Lets framework
//     extensions register custom rules (e.g. SSN, phone) without
//     forking the log package.
//   - LOG_REDACT=true env: process-wide default-on. Honoured even
//     when cfg.Config has no "redact" key so a single env flip in a
//     stricter compliance environment covers every channel.
//
// Returns inner unchanged when no opt-in is in effect so the common
// path stays allocation-free.
func wrapWithRedactors(inner Logger, cfg LogConfig) Logger {
	if inner == nil {
		return inner
	}
	if rs, ok := cfg.Config["redactors"].([]Redactor); ok && len(rs) > 0 {
		return WithRedactors(inner, rs...)
	}
	if enabled, ok := cfg.Config["redact"].(bool); ok && enabled {
		return WithRedactors(inner, BuildDefaultRedactors())
	}
	if strings.EqualFold(os.Getenv("LOG_REDACT"), "true") {
		return WithRedactors(inner, BuildDefaultRedactors())
	}
	return inner
}
