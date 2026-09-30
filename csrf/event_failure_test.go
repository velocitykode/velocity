package csrf

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	logdrivers "github.com/velocitykode/velocity/log/drivers"
)

// A failed event dispatch goes to the one failure policy: counted, and the
// first failure of each event name logged at warn level through the
// component's logger (the standalone fallback here), not ignored.
func TestCSRF_FailedEventDispatchLoggedOncePerEvent(t *testing.T) {
	out := fallbacklogtest.Capture(t)
	c := &CSRF{}
	c.SetEventDispatcher(func(context.Context, interface{}) error { return errors.New("listener failed") })
	for i := 0; i < 2; i++ {
		c.dispatchSessionMissing(httptest.NewRequest(http.MethodPost, "/form", nil))
	}
	if n := strings.Count(out.String(), "WARN event dispatch failed"); n != 1 || !strings.Contains(out.String(), "event=csrf.session.missed") {
		t.Errorf("fallback output = %q, want one warn line naming csrf.session.missed", out.String())
	}
}

// With a logger installed, the failure line goes through it, not the
// fallback; installing the logger after the dispatcher works too.
func TestCSRF_FailedEventDispatchLogsThroughItsLogger(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	var buf lockedLines
	c := &CSRF{}
	c.SetEventDispatcher(func(context.Context, interface{}) error { return errors.New("listener failed") })
	c.SetLogger(logdrivers.NewConsoleLoggerTo(&buf, contract.LogLevelUnset))
	c.dispatchSessionMissing(httptest.NewRequest(http.MethodPost, "/form", nil))
	if got := buf.String(); strings.Count(got, "event dispatch failed") != 1 || !strings.Contains(got, "event=csrf.session.missed") {
		t.Errorf("component logger got %q, want one failure line naming csrf.session.missed", got)
	}
	if got := fallback.String(); got != "" {
		t.Errorf("fallback got %q, want nothing", got)
	}
}

// lockedLines is an io.Writer safe for concurrent writes.
type lockedLines struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedLines) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedLines) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
