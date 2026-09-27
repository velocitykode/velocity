package log

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

func TestNewLogger_ConsoleDriver(t *testing.T) {
	logger, err := NewLogger(LogConfig{
		Driver: "console",
		Config: map[string]any{},
	})
	if err != nil {
		t.Fatalf("NewLogger(console) error = %v", err)
	}
	if logger == nil {
		t.Fatal("NewLogger(console) returned nil")
	}
}

func TestNewLogger_EmptyDriverDefaultsToConsole(t *testing.T) {
	// An empty Driver must default to the always-registered "console"
	// driver rather than erroring on an unknown-driver lookup, so a
	// zero-value LogConfig produces a working logger.
	logger, err := NewLogger(LogConfig{Config: map[string]any{}})
	if err != nil {
		t.Fatalf("NewLogger(empty driver) error = %v", err)
	}
	if logger == nil {
		t.Fatal("NewLogger(empty driver) returned nil")
	}
	// Should not panic and should behave like a console logger.
	logger.Info("empty-driver defaulted to console")
}

func TestNewLogger_FileDriver(t *testing.T) {
	tests := []struct {
		name   string
		config LogConfig
	}{
		{
			name: "file driver with path",
			config: LogConfig{
				Driver: "file",
				Config: map[string]any{"path": "/tmp/test-logs"},
			},
		},
		{
			name: "file driver without path",
			config: LogConfig{
				Driver: "file",
				Config: map[string]any{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, err := NewLogger(tt.config)
			if err != nil {
				t.Fatalf("NewLogger() error = %v", err)
			}
			if logger == nil {
				t.Fatal("NewLogger() returned nil")
			}
		})
	}
}

func TestNewLogger_InvalidDriver(t *testing.T) {
	_, err := NewLogger(LogConfig{Driver: "invalid_driver_that_will_fail"})
	if err == nil {
		t.Error("Expected error for invalid driver")
	}
}

func TestNewLogger_UnsupportedDriver(t *testing.T) {
	_, err := NewLogger(LogConfig{
		Driver: "unknown",
		Config: map[string]any{},
	})
	if err == nil {
		t.Error("Expected error for unsupported driver")
	}
}

func TestLoggerMethods_DoNotPanic(t *testing.T) {
	logger, err := NewLogger(LogConfig{
		Driver: "console",
		Config: map[string]any{},
	})
	if err != nil {
		t.Fatalf("NewLogger() error = %v", err)
	}

	// These should not panic
	logger.Debug("test debug message", "key", "value")
	logger.Info("test info message", "key", "value")
	logger.Warn("test warn message", "key", "value")
	logger.Error("test error message", "key", "value")
}

func TestLoggerFatal(t *testing.T) {
	// Test that the Logger interface includes Fatal method
	// We use a mock to avoid os.Exit
	called := false
	mock := &mockLogger{
		onFatal: func(msg string, kvs ...any) {
			called = true
		},
	}

	var logger Logger = mock
	logger.Fatal("test", "key", "value")

	if !called {
		t.Error("Fatal should call the logger's Fatal method")
	}
}

type mockLogger struct {
	onFatal func(string, ...any)
}

func (m *mockLogger) Debug(msg string, kvs ...any) {}
func (m *mockLogger) Info(msg string, kvs ...any)  {}
func (m *mockLogger) Warn(msg string, kvs ...any)  {}
func (m *mockLogger) Error(msg string, kvs ...any) {}
func (m *mockLogger) Fatal(msg string, kvs ...any) {
	if m.onFatal != nil {
		m.onFatal(msg, kvs...)
	}
}

func (m *mockLogger) With(kvs ...any) contract.Logger { return contract.BindFields(m, kvs...) }

func TestNewLogger_NullDriver(t *testing.T) {
	logger, err := NewLogger(LogConfig{
		Driver: "null",
		Config: map[string]any{},
	})
	if err != nil {
		t.Fatalf("NewLogger(null) error = %v", err)
	}
	// Should not panic
	logger.Debug("ignored")
	logger.Info("ignored")
}

func TestNewLogger_StackDriver(t *testing.T) {
	logger, err := NewLogger(LogConfig{
		Driver: "stack",
		Config: map[string]any{
			"stack": []string{"console", "null"},
		},
	})
	if err != nil {
		t.Fatalf("NewLogger(stack) error = %v", err)
	}
	// Should not panic
	logger.Info("test message", "key", "value")
}

func TestNewLogger_StackDriver_DefaultChannels(t *testing.T) {
	tempDir := t.TempDir()
	logger, err := NewLogger(LogConfig{
		Driver: "stack",
		Config: map[string]any{
			"path": tempDir,
			"days": 0,
		},
	})
	if err != nil {
		t.Fatalf("NewLogger(stack) error = %v", err)
	}
	logger.Info("test default stack")
}

func TestNewLogger_StackDriver_SkipsRecursion(t *testing.T) {
	logger, err := NewLogger(LogConfig{
		Driver: "stack",
		Config: map[string]any{
			"stack": []string{"stack", "console"},
		},
	})
	if err != nil {
		t.Fatalf("NewLogger(stack) error = %v", err)
	}
	logger.Info("no recursion")
}

func TestNewLogger_StackDriver_NoValidChannels(t *testing.T) {
	_, err := NewLogger(LogConfig{
		Driver: "stack",
		Config: map[string]any{
			"stack": []string{"stack"},
		},
	})
	if err == nil {
		t.Error("Expected error when all channels are invalid")
	}
}

func TestStackLogger_Shutdown(t *testing.T) {
	closed := 0
	mock1 := &mockShutdowner{onShutdown: func() error { closed++; return nil }}
	mock2 := &mockShutdowner{onShutdown: func() error { closed++; return nil }}
	stack := NewStackLogger(mock1, mock2)

	if err := stack.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if closed != 2 {
		t.Errorf("expected 2 Shutdown calls, got %d", closed)
	}
}

type mockShutdowner struct {
	mockLogger
	onShutdown func() error
}

func (m *mockShutdowner) Shutdown(_ context.Context) error {
	if m.onShutdown != nil {
		return m.onShutdown()
	}
	return nil
}

func TestLoggerLevels(t *testing.T) {
	levels := []Level{DEBUG, INFO, WARN, ERROR, FATAL}
	expected := []int{0, 1, 2, 3, 4}

	for i, level := range levels {
		if int(level) != expected[i] {
			t.Errorf("Level %d = %d, want %d", i, level, expected[i])
		}
	}
}

// stackChild records the pairs of its last Info line and whether it was
// shut down.
type stackChild struct {
	kvs      []any
	shutdown bool
}

func (c *stackChild) Debug(string, ...any)            {}
func (c *stackChild) Info(_ string, kvs ...any)       { c.kvs = kvs }
func (c *stackChild) Warn(string, ...any)             {}
func (c *stackChild) Error(string, ...any)            {}
func (c *stackChild) Fatal(string, ...any)            {}
func (c *stackChild) With(kvs ...any) contract.Logger { return contract.BindFields(c, kvs...) }
func (c *stackChild) Shutdown(context.Context) error  { c.shutdown = true; return nil }

// A stack's With binds the pairs on every child, and the stack it returns
// does not shut its children down.
func TestStackLogger_With(t *testing.T) {
	a, b := &stackChild{}, &stackChild{}
	bound := NewStackLogger(a, b).With("request_id", "r1")

	bound.Info("m", "k", "v")
	if err := bound.(Shutdowner).Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	for name, child := range map[string]*stackChild{"a": a, "b": b} {
		if got := child.kvs; len(got) != 4 || got[0] != "request_id" || got[1] != "r1" || got[2] != "k" || got[3] != "v" {
			t.Errorf("child %s got %v, want request_id=r1 then k=v", name, got)
		}
		if child.shutdown {
			t.Errorf("child %s shut down by the bound stack", name)
		}
	}
}

// NullLogger.With returns the null logger.
func TestNullLogger_With(t *testing.T) {
	n := NewNullLogger()
	if got := n.With("k", "v"); got != Logger(n) {
		t.Errorf("With = %T, want the null logger", got)
	}
}
