package velocity

import (
	"bytes"
	stdlog "log"
	"log/slog"
	"net/http"
	"strings"
	"testing"

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

// Without WithMaintenanceLogger the warning goes to slog.Default() as it
// stood when the middleware was built.
func TestPreventRequestsDuringMaintenance_WarnsThroughSlogDefaultAtConstruction(t *testing.T) {
	useTempMaintRoot(t)
	prev, prevOut, prevFlags := slog.Default(), stdlog.Writer(), stdlog.Flags()
	t.Cleanup(func() {
		slog.SetDefault(prev)
		stdlog.SetOutput(prevOut)
		stdlog.SetFlags(prevFlags)
	})

	var built, later bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&built, nil)))
	mw := PreventRequestsDuringMaintenance()
	slog.SetDefault(slog.New(slog.NewTextHandler(&later, nil)))

	runMaintenanceOnce(t, mw)

	if !strings.Contains(built.String(), "maintenance marker path resolved") {
		t.Errorf("slog default at construction got %q, want the marker-path warning", built.String())
	}
	if later.Len() != 0 {
		t.Errorf("slog default at request time got %q, want nothing", later.String())
	}
}

// TestPreventRequestsDuringMaintenance_SlogDefaultKeepsWarnLevelAndSource
// pins that the marker-path warning reaches the slog default captured at
// construction exactly as a direct *slog.Logger Warn call from maintenance.go
// would write it: warn level, the same attributes, and a source naming
// maintenance.go rather than the adapter that carries the line.
func TestPreventRequestsDuringMaintenance_SlogDefaultKeepsWarnLevelAndSource(t *testing.T) {
	useTempMaintRoot(t)
	prev, prevOut, prevFlags := slog.Default(), stdlog.Writer(), stdlog.Flags()
	t.Cleanup(func() {
		slog.SetDefault(prev)
		stdlog.SetOutput(prevOut)
		stdlog.SetFlags(prevFlags)
	})

	var out bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{AddSource: true})))
	runMaintenanceOnce(t, PreventRequestsDuringMaintenance())

	line := out.String()
	for _, want := range []string{"level=WARN", `msg="maintenance marker path resolved"`, "path=", "source="} {
		if !strings.Contains(line, want) {
			t.Errorf("warning %q lacks %q", line, want)
		}
	}
	if !strings.Contains(line, "/maintenance.go:") || strings.Contains(line, "slog_logger.go") {
		t.Errorf("warning %q, want its source in maintenance.go", line)
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
