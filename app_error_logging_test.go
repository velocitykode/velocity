package velocity

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/router"
)

// errCaptureLogger is a log.Logger that records Error calls. Non-error levels
// are accepted and dropped so boot-time Warn/Info noise does not affect
// the assertions.
type errCaptureLogger struct {
	mu     sync.Mutex
	errors []errCapturedEntry
}

type errCapturedEntry struct {
	msg string
	kvs []any
}

func (l *errCaptureLogger) Debug(string, ...any) {}
func (l *errCaptureLogger) Info(string, ...any)  {}
func (l *errCaptureLogger) Warn(string, ...any)  {}
func (l *errCaptureLogger) Fatal(string, ...any) {}

func (l *errCaptureLogger) Error(msg string, kvs ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errors = append(l.errors, errCapturedEntry{msg: msg, kvs: kvs})
}

func (l *errCaptureLogger) errorCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.errors)
}

func (l *errCaptureLogger) lastError() errCapturedEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.errors[len(l.errors)-1]
}

// newCaptureApp builds a test app whose a.Log is the returned errCaptureLogger,
// installed via a log-driver override so the logger is in place during New()
// (the exceptions LogReporter snapshots a.Log at construction time).
func newCaptureApp(t *testing.T) (*App, *errCaptureLogger) {
	t.Helper()
	capture := &errCaptureLogger{}
	const driverName = "v215-capture"
	prev := log.Drivers().Override(driverName, func(_ context.Context, _ log.LogConfig) (log.Logger, error) {
		return capture, nil
	})
	t.Cleanup(func() { log.Drivers().Override(driverName, prev) })

	a, err := New(WithConfig(Config{
		Env:   "testing",
		Debug: true,
		Port:  "0",
		Cache: CacheConfig{Driver: "memory", Prefix: "test_cache"},
		Log:   log.LogConfig{Driver: driverName, Config: make(map[string]any)},
		Queue: QueueConfig{Driver: "memory"},
		Mail:  mail.MailConfig{Driver: "log"},
	}))
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	return a, capture
}

// V2-15: the exceptions handler built by New() must report through the app
// logger out of the box (previously its default LogReporter had a nil logger
// and silently dropped every Report).
func TestNew_ExceptionsReporterLogsViaAppLogger(t *testing.T) {
	a, capture := newCaptureApp(t)

	a.Services.Exceptions.Report(errors.New("reported failure"), nil)

	if capture.errorCount() != 1 {
		t.Fatalf("expected exactly 1 error log entry, got %d", capture.errorCount())
	}
	if got := capture.lastError().msg; !strings.Contains(got, "reported failure") {
		t.Errorf("expected reported error message, got %q", got)
	}
}

// V2-15: a handler returning a 500-class error must produce exactly one
// error-level log entry by default; a recovered panic likewise (with stack).
func TestNew_RouterDefaultErrorPathLogs(t *testing.T) {
	a, capture := newCaptureApp(t)

	a.Router.Get("/boom", func(c *router.Context) error {
		return errors.New("handler blew up")
	})
	a.Router.Get("/panic", func(c *router.Context) error {
		panic("handler panicked")
	})

	w := httptest.NewRecorder()
	a.Router.ServeHTTP(w, httptest.NewRequest("GET", "/boom", nil))
	if w.Code != 500 {
		t.Errorf("expected 500, got %d", w.Code)
	}
	if capture.errorCount() != 1 {
		t.Fatalf("expected exactly 1 error log entry after handler error, got %d", capture.errorCount())
	}

	w = httptest.NewRecorder()
	a.Router.ServeHTTP(w, httptest.NewRequest("GET", "/panic", nil))
	if w.Code != 500 {
		t.Errorf("expected 500, got %d", w.Code)
	}
	if capture.errorCount() != 2 {
		t.Fatalf("expected exactly 2 error log entries after panic, got %d", capture.errorCount())
	}
	entry := capture.lastError()
	var hasStack bool
	for i := 0; i+1 < len(entry.kvs); i += 2 {
		if entry.kvs[i] == "stack" {
			hasStack = entry.kvs[i+1].(string) != ""
		}
	}
	if !hasStack {
		t.Error("panic log entry must include a non-empty stack")
	}
}

// V2-15: a consumer-installed ErrorHandler fully replaces the default error
// pipeline, including the default logging.
func TestNew_RouterCustomErrorHandlerSuppressesDefaultLogging(t *testing.T) {
	a, capture := newCaptureApp(t)

	a.Router.ErrorHandler = func(c *router.Context, err error) {
		c.Response.WriteHeader(502)
	}
	a.Router.Get("/boom", func(c *router.Context) error {
		return errors.New("consumer owns this")
	})

	w := httptest.NewRecorder()
	a.Router.ServeHTTP(w, httptest.NewRequest("GET", "/boom", nil))

	if w.Code != 502 {
		t.Errorf("expected 502 from custom handler, got %d", w.Code)
	}
	if capture.errorCount() != 0 {
		t.Fatalf("custom ErrorHandler must suppress default logging, got %d entries", capture.errorCount())
	}
}
