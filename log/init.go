package log

import (
	"context"
	"os"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/driverregistry"
	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/nilval"
	"github.com/velocitykode/velocity/log/drivers"
)

// Config holds configuration for creating a Logger instance.
type Config struct {
	// Config holds driver-specific options (e.g., "path" for file driver,
	// "stack" with []string of channel names for the stack driver, "level"
	// for any driver). ConfigFromEnv fills "path" (LOG_PATH, set only when
	// non-empty), "days" (LOG_DAYS, default 14), "level" (LOG_LEVEL, default
	// "debug"), "redact" (LOG_REDACT, set only when true), "redact_emails"
	// (LOG_REDACT_EMAILS, set only when true) and "stack" (LOG_STACK,
	// comma-separated, set only when non-empty).
	Config map[string]any

	// Driver specifies the log driver: "console", "file", "stack", "null", or
	// any third-party driver registered via Drivers().Register (LOG_DRIVER,
	// default "console").
	Driver string
}

// Validate checks that Driver names a registered driver (empty is accepted:
// NewLogger treats it as "console") and, for the stack driver, that every
// channel in the "stack" list does too, so an unknown or unimported driver
// fails at boot. Per-driver field validation (path for file) lives in each
// driver's factory because the registry is open for third-party drivers.
func (c Config) Validate() error {
	if c.Driver != "" {
		if err := driverRegistry.Check(c.Driver); err != nil {
			return err
		}
	}
	if driverregistry.Normalise(c.Driver) == "stack" {
		channels, _ := c.Config["stack"].([]string)
		for _, name := range channels {
			if err := driverRegistry.Check(name); err != nil {
				return errchain.Errorf("velocity/log: stack channel %q: %w", name, err)
			}
		}
	}
	return nil
}

// driverRegistry is the canonical Velocity driver registry for loggers.
// The light root drivers (console, null) self-register from this file's
// init(); the file/daily and stack drivers self-register from the log/file
// and log/stack leaves. Third-party drivers can register additional
// factories.
var driverRegistry = driverregistry.New[Logger, Config]("log")

// Drivers returns the registry that log driver factories register
// themselves into. Use this from a driver package's init() to install a
// factory:
//
//	func init() {
//	    log.Drivers().Register("syslog", func(_ context.Context, cfg log.Config) (log.Logger, error) {
//	        return newSyslogLogger(cfg.Config), nil
//	    })
//	}
func Drivers() *driverregistry.Registry[Logger, Config] { return driverRegistry }

// init wires the framework's built-in light log drivers into the canonical
// driver registry. Registrations happen at package import time so that
// importing log anywhere (directly or transitively) makes console and null
// available without a blank import, which keeps the console default working
// zero-config. Third-party drivers can register via log.Drivers().Register.
//
// The file, daily, and stack drivers are NOT registered here. The file/daily
// pair lives in the log/file leaf (which carries the async dependency) and
// stack lives in the log/stack leaf; each self-registers from its own init().
// Blank-import github.com/velocitykode/velocity/log/file,
// github.com/velocitykode/velocity/log/stack, or the aggregator
// github.com/velocitykode/velocity/log/standard to enable them.
func init() {
	Drivers().Register("console", func(_ context.Context, cfg Config) (Logger, error) {
		// Optional "writer": "stderr" moves output off stdout so stdout
		// can carry machine-readable command output (vel routes --json).
		if w, ok := cfg.Config["writer"].(string); ok && w == "stderr" {
			return WrapWithRedactors(drivers.NewConsoleLoggerTo(os.Stderr, ExtractLevel(cfg.Config)), cfg), nil
		}
		return WrapWithRedactors(drivers.NewConsoleLogger(ExtractLevel(cfg.Config)), cfg), nil
	})

	Drivers().Register("null", func(_ context.Context, _ Config) (Logger, error) {
		return NewNullLogger(), nil
	})
}

// ExtractLevel pulls a "level" string from a driver config map and returns
// it as a contract.LogLevel, defaulting to contract.LogLevelDebug when
// absent or unrecognised. It is exported so leaf driver packages
// (log/file, log/stack) derive the same level the root drivers do.
func ExtractLevel(config map[string]any) contract.LogLevel {
	if config == nil {
		return contract.LogLevelDebug
	}
	if l, ok := config["level"].(string); ok {
		return parseLevel(l)
	}
	return contract.LogLevelDebug
}

// WrapWithRedactors layers the default redactor chain on top of a
// freshly constructed driver Logger when the config opts in:
//
//   - cfg.Config["redact"] == true: per-channel JSON-friendly toggle.
//   - cfg.Config["redactors"] []Redactor: caller-supplied chain that
//     bypasses BuildDefaultRedactors entirely. Lets framework
//     extensions register custom rules (e.g. SSN, phone) without
//     forking the log package.
//   - cfg.Config["redact_emails"] == true: adds EmailRedactor to the
//     default chain.
//
// velocity.ConfigFromEnv sets "redact" from LOG_REDACT=true and
// "redact_emails" from LOG_REDACT_EMAILS=true. The registered "stack"
// driver passes its Config to every child, so one flag covers every
// channel of that stack; a log.Manager channel redacts only when its own
// Options carry the flags.
//
// Returns inner unchanged when no opt-in is in effect so the common
// path stays allocation-free, and nil for a nil inner, typed or not.
func WrapWithRedactors(inner Logger, cfg Config) Logger {
	if nilval.Is(inner) {
		return nil
	}
	if rs, ok := cfg.Config["redactors"].([]Redactor); ok && len(rs) > 0 {
		return WithRedactors(inner, rs...)
	}
	if enabled, ok := cfg.Config["redact"].(bool); ok && enabled {
		emails, _ := cfg.Config["redact_emails"].(bool)
		return WithRedactors(inner, BuildDefaultRedactors(emails))
	}
	return inner
}
