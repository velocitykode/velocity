package log

import (
	"context"
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

// Validate checks structural requirements: empty Driver is treated as
// "console" by NewLogger so it is accepted. Per-driver field validation
// (path for file, stack list for stack) lives in each driver's factory
// because the registry is open for third-party drivers; LogConfig.Validate
// does not introspect the Config map.
func (c LogConfig) Validate() error {
	// Reserved for future structural checks. Driver name resolution is
	// driver-registry's job (Drivers().Resolve returns a typed error when
	// the name is unknown), so we do not duplicate that allowlist here.
	return nil
}

// driverRegistry is the canonical Velocity driver registry for loggers.
// The light root drivers (console, null) self-register from this file's
// init(); the file/daily and stack drivers self-register from the log/file
// and log/stack leaves. Third-party drivers can register additional
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
	Drivers().Register("console", func(_ context.Context, cfg LogConfig) (Logger, error) {
		// Optional "writer": "stderr" moves output off stdout so stdout
		// can carry machine-readable command output (vel routes --json).
		if w, ok := cfg.Config["writer"].(string); ok && w == "stderr" {
			return WrapWithRedactors(drivers.NewConsoleLoggerTo(os.Stderr, ExtractLevel(cfg.Config)), cfg), nil
		}
		return WrapWithRedactors(drivers.NewConsoleLogger(ExtractLevel(cfg.Config)), cfg), nil
	})

	Drivers().Register("null", func(_ context.Context, _ LogConfig) (Logger, error) {
		return NewNullLogger(), nil
	})
}

// ExtractLevel pulls a "level" string from a driver config map and returns
// its numeric value, defaulting to DEBUG when absent or unrecognised. It is
// exported so leaf driver packages (log/file, log/stack) derive the same
// level the root drivers do.
func ExtractLevel(config map[string]any) int {
	if config == nil {
		return int(DEBUG)
	}
	if l, ok := config["level"].(string); ok {
		return parseLevel(l)
	}
	return int(DEBUG)
}

// WrapWithRedactors layers the default redactor chain on top of a
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
func WrapWithRedactors(inner Logger, cfg LogConfig) Logger {
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
