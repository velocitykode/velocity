package router

import (
	"errors"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/app"
)

// nilLogger is a contract.Logger implementation whose nil pointer is a
// typed nil: a service slot holding one is absent.
type nilLogger struct{ levelLogger }

// A typed-nil logger, the router's own or its services', is absent: the
// event failure path falls back instead of calling through it.
func TestEventLogger_TypedNilIsAbsent(t *testing.T) {
	r := NewV2()
	r.SetLogger((*nilLogger)(nil))
	r.SetServices(&app.Services{Log: (*nilLogger)(nil)})
	if l := r.eventLogger(); l != nil {
		t.Fatalf("eventLogger = %#v, want nil for typed-nil loggers", l)
	}
}

// A late Timeout panic on a Context whose services hold a typed-nil
// error handler is logged as an unreported panic, not handed to the nil
// handler (whose call would panic).
func TestReportLatePanic_TypedNilErrorHandlerIsAbsent(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	c, _ := NewTestContext("GET", "/")
	c.Request = httptest.NewRequest("GET", "/", nil)
	c.services = &app.Services{
		Errors: (*fakeErrorHandler)(nil),
		Log: levelLogger{onError: func(msg string, _ ...any) {
			mu.Lock()
			lines = append(lines, msg)
			mu.Unlock()
		}},
	}
	reportLatePanic(c, newPanicError(errors.New("late"), 0))
	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 1 || lines[0] != "velocity/router: timeout handler panicked after the timeout answered" {
		t.Fatalf("lines = %q, want the unreported-panic line once", lines)
	}
}
