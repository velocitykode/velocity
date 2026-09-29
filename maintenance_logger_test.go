package velocity

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

// WithMaintenanceLogger takes the app's contract.Logger and receives the
// one-time marker-path warning at warn level.
func TestWithMaintenanceLogger_ReceivesMarkerPathWarning(t *testing.T) {
	root := useTempMaintRoot(t)
	logs := &levelLogger{}

	runMaintenanceOnce(t, PreventRequestsDuringMaintenance(WithMaintenanceLogger(logs)))

	if got := entriesWith(logs, "warn", "maintenance marker path resolved"); got != 1 {
		t.Fatalf("warn entries = %d, want 1 (%+v)", got, logs.entries)
	}
	if kvs := logs.entries[0].kvs; len(kvs) < 2 || kvs[0] != "path" || !strings.HasPrefix(kvs[1].(string), root) {
		t.Errorf("kvs = %v, want the marker path under %s", kvs, root)
	}
}

// Without WithMaintenanceLogger (or with a nil one) the warning goes
// through the one framework fallback logger, and nothing through the
// standard library log package or slog.Default.
func TestPreventRequestsDuringMaintenance_WithoutLoggerWarnsThroughTheFallback(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []MaintenanceOption
	}{
		{"no option", nil},
		{"nil logger", []MaintenanceOption{WithMaintenanceLogger(nil)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useTempMaintRoot(t)
			fallback := fallbacklogtest.Capture(t)
			stdlib := fallbacklogtest.CaptureStdlib(t)

			runMaintenanceOnce(t, PreventRequestsDuringMaintenance(tc.opts...))

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

// Inside an app, the middleware without WithMaintenanceLogger writes the
// marker-path warning through the request's app logger, not the fallback.
func TestPreventRequestsDuringMaintenance_InAppWarnsThroughTheAppLogger(t *testing.T) {
	useTempMaintRoot(t)
	fallback := fallbacklogtest.Capture(t)
	a, capture := newLoggerWiringApp(t, nil)
	a.Router.Use(PreventRequestsDuringMaintenance())
	a.Router.Get("/", func(c *router.Context) error { return c.String(http.StatusOK, "ok") })

	rec := httptest.NewRecorder()
	a.Router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := entriesWith(capture, "warn", "maintenance marker path resolved"); got != 1 {
		t.Errorf("app logger warn entries = %d, want 1", got)
	}
	if got := fallback.Count("WARN", "maintenance marker path resolved"); got != 0 {
		t.Errorf("fallback lines = %d, want 0: %q", got, fallback.String())
	}
}
