package maintenance

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/router"
)

// runMaintenanceOnce sends one request through mw.
func runMaintenanceOnce(t *testing.T, mw router.MiddlewareFunc) {
	t.Helper()
	c, _ := router.NewTestContext(http.MethodGet, "/")
	if err := mw(func(*router.Context) error { return nil })(c); err != nil {
		t.Fatalf("middleware: %v", err)
	}
}

// WithLogger takes the app's contract.Logger and receives the
// one-time marker-path warning at warn level.
func TestWithLogger_ReceivesMarkerPathWarning(t *testing.T) {
	root := useTempMaintRoot(t)
	logs := &levelLogger{}

	runMaintenanceOnce(t, Middleware(Config{}, WithLogger(logs)))

	if got := entriesWith(logs, "warn", "maintenance marker path resolved"); got != 1 {
		t.Fatalf("warn entries = %d, want 1 (%+v)", got, logs.entries)
	}
	if kvs := logs.entries[0].kvs; len(kvs) < 2 || kvs[0] != "path" || !strings.HasPrefix(kvs[1].(string), root) {
		t.Errorf("kvs = %v, want the marker path under %s", kvs, root)
	}
}

// Without WithLogger (or with a nil one) the warning goes
// through the one framework fallback logger, and nothing through the
// standard library log package or slog.Default.
func TestMiddleware_WithoutLoggerWarnsThroughTheFallback(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []Option
	}{
		{"no option", nil},
		{"nil logger", []Option{WithLogger(nil)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useTempMaintRoot(t)
			fallback := fallbacklogtest.Capture(t)
			stdlib := fallbacklogtest.CaptureStdlib(t)

			runMaintenanceOnce(t, Middleware(Config{}, tc.opts...))

			if got := fallback.Count("WARN", "maintenance marker path resolved"); got != 1 {
				t.Errorf("fallback lines = %d, want 1: %q", got, fallback.String())
			}
			if out := stdlib.String(); out != "" {
				t.Errorf("stdlib log / slog.Default got %q, want nothing", out)
			}
		})
	}
}

// entriesWith counts l's entries with msg, at level when level is set.
func entriesWith(l *levelLogger, level, msg string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, e := range l.entries {
		if e.msg == msg && (level == "" || e.level == level) {
			n++
		}
	}
	return n
}

// Without WithLogger, the middleware writes the marker-path warning
// through the request's app logger (Services.Log), not the fallback.
func TestMiddleware_WarnsThroughTheRequestAppLogger(t *testing.T) {
	useTempMaintRoot(t)
	fallback := fallbacklogtest.Capture(t)
	capture := &levelLogger{}
	c, _ := router.NewTestContext(http.MethodGet, "/")
	c.SetServices(&app.Services{Log: capture})
	if err := Middleware(Config{})(func(*router.Context) error { return nil })(c); err != nil {
		t.Fatalf("middleware: %v", err)
	}

	if got := entriesWith(capture, "warn", "maintenance marker path resolved"); got != 1 {
		t.Errorf("app logger warn entries = %d, want 1", got)
	}
	if got := fallback.Count("WARN", "maintenance marker path resolved"); got != 0 {
		t.Errorf("fallback lines = %d, want 0: %q", got, fallback.String())
	}
}

// levelLogger records every entry with its level.
type levelLogger struct {
	mu      sync.Mutex
	entries []levelEntry
}

type levelEntry struct {
	level string
	msg   string
	kvs   []any
}

func (l *levelLogger) Debug(msg string, kvs ...any) { l.add("debug", msg, kvs) }
func (l *levelLogger) Info(msg string, kvs ...any)  { l.add("info", msg, kvs) }
func (l *levelLogger) Warn(msg string, kvs ...any)  { l.add("warn", msg, kvs) }
func (l *levelLogger) Error(msg string, kvs ...any) { l.add("error", msg, kvs) }
func (l *levelLogger) Fatal(msg string, kvs ...any) { l.add("fatal", msg, kvs) }

func (l *levelLogger) With(kvs ...any) contract.Logger { return contract.BindFields(l, kvs...) }

func (l *levelLogger) add(level, msg string, kvs []any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, levelEntry{level: level, msg: msg, kvs: kvs})
}
