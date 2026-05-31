package log_test

import (
	"path/filepath"
	"testing"

	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/log/drivers"
	"github.com/velocitykode/velocity/log/file"
	"github.com/velocitykode/velocity/log/logtest"
)

// TestConsoleLogger_Contract runs the logtest spec against the console
// logger. Output goes to stdout under the test runner; that is fine for a
// contract test because we only assert non-panicking behaviour, not output
// shape.
func TestConsoleLogger_Contract(t *testing.T) {
	logtest.RunLoggerContractTests(t, func(t *testing.T) log.Logger {
		return drivers.NewConsoleLogger(0) // debug-and-above
	})
}

// TestFileLogger_Contract runs the logtest spec against the file logger
// rooted at t.TempDir to avoid polluting the working directory.
func TestFileLogger_Contract(t *testing.T) {
	logtest.RunLoggerContractTests(t, func(t *testing.T) log.Logger {
		path := filepath.Join(t.TempDir(), "contract.log")
		return file.NewFileLogger(path, 7, 0)
	})
}

// TestNullLogger_Contract runs the logtest spec against the no-op logger.
// The null logger drops every record; the invariants we check (no panic,
// no error) are exactly the ones a no-op must preserve.
func TestNullLogger_Contract(t *testing.T) {
	logtest.RunLoggerContractTests(t, func(t *testing.T) log.Logger {
		return log.NewNullLogger()
	})
}

// TestStackLogger_Contract runs the logtest spec against the stack logger
// (fan-out to multiple children). The children are a null logger and a
// file logger rooted at t.TempDir, so output is captured but the runner
// only asserts non-panicking fan-out.
func TestStackLogger_Contract(t *testing.T) {
	logtest.RunLoggerContractTests(t, func(t *testing.T) log.Logger {
		path := filepath.Join(t.TempDir(), "stack.log")
		return log.NewStackLogger(
			log.NewNullLogger(),
			file.NewFileLogger(path, 7, 0),
		)
	})
}

// Registry-driven contract tests. Each of these constructs a logger via
// log.NewLogger / log.Drivers().Resolve, exercising the registry mapping
// in log/init.go rather than the constructor directly. A missing or
// misnamed registry entry would surface here even when the underlying
// driver still passes its own constructor-based contract test.

func TestRegistry_ConsoleDriver_Contract(t *testing.T) {
	logtest.RunLoggerContractTests(t, func(t *testing.T) log.Logger {
		l, err := log.NewLogger(log.LogConfig{Driver: "console"})
		if err != nil {
			t.Fatalf("registry resolve console: %v", err)
		}
		return l
	})
}

func TestRegistry_FileDriver_Contract(t *testing.T) {
	logtest.RunLoggerContractTests(t, func(t *testing.T) log.Logger {
		l, err := log.NewLogger(log.LogConfig{
			Driver: "file",
			Config: map[string]any{
				"path": filepath.Join(t.TempDir(), "file.log"),
				"days": 7,
			},
		})
		if err != nil {
			t.Fatalf("registry resolve file: %v", err)
		}
		return l
	})
}

// TestRegistry_DailyDriver_Contract verifies the "daily" driver name is
// wired through the registry (log/init.go:59). Catches a broken or missing
// alias mapping that the direct NewFileLogger test cannot.
func TestRegistry_DailyDriver_Contract(t *testing.T) {
	logtest.RunLoggerContractTests(t, func(t *testing.T) log.Logger {
		l, err := log.NewLogger(log.LogConfig{
			Driver: "daily",
			Config: map[string]any{
				"path": filepath.Join(t.TempDir(), "daily.log"),
				"days": 7,
			},
		})
		if err != nil {
			t.Fatalf("registry resolve daily: %v", err)
		}
		return l
	})
}

func TestRegistry_NullDriver_Contract(t *testing.T) {
	logtest.RunLoggerContractTests(t, func(t *testing.T) log.Logger {
		l, err := log.NewLogger(log.LogConfig{Driver: "null"})
		if err != nil {
			t.Fatalf("registry resolve null: %v", err)
		}
		return l
	})
}

func TestRegistry_StackDriver_Contract(t *testing.T) {
	logtest.RunLoggerContractTests(t, func(t *testing.T) log.Logger {
		l, err := log.NewLogger(log.LogConfig{
			Driver: "stack",
			Config: map[string]any{
				// Explicit child list so the stack does not pull in
				// the default "console" + "daily" pair, which would
				// require a writable storage/logs dir.
				"stack": []string{"null"},
			},
		})
		if err != nil {
			t.Fatalf("registry resolve stack: %v", err)
		}
		return l
	})
}
