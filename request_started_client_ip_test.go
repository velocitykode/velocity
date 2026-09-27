package velocity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/router"
)

// funcListener is a synchronous listener that hands every event to fn.
type funcListener func(event any)

func (l funcListener) Handle(_ context.Context, event any) error {
	l(event)
	return nil
}

func (l funcListener) Async() bool { return false }

// TestRequestStarted_CarriesTheReportedClientIP serves a failing request
// through a trusted proxy in a bootstrapped app: RequestStarted carries the
// client IP the error report carries, beside the raw peer address.
func TestRequestStarted_CarriesTheReportedClientIP(t *testing.T) {
	a, err := New(WithConfig(Config{
		Env:   "testing",
		Port:  "0",
		Cache: CacheConfig{Driver: "memory", Prefix: "test_cache"},
		Log:   log.LogConfig{Driver: "null", Config: make(map[string]any)},
		Queue: QueueConfig{Driver: "memory"},
		Mail:  mail.MailConfig{Driver: "log"},
		Auth:  auth.Config{TrustedProxies: []string{"10.0.0.0/8"}},
	}))
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	reports := &recordingReporter{}
	a.Services.Errors.AddReporter(reports)

	var mu sync.Mutex
	var startedEvents []*router.RequestStarted
	a.Services.Events.Listen(events.OfType[*router.RequestStarted](), funcListener(func(event any) {
		mu.Lock()
		defer mu.Unlock()
		startedEvents = append(startedEvents, event.(*router.RequestStarted))
	}))
	a.Router.Get("/boom", func(*router.Context) error { return errors.New("boom") })

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	req.RemoteAddr = "10.1.2.3:4567"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	a.Router.ServeHTTP(httptest.NewRecorder(), req)

	if reports.count() != 1 {
		t.Fatalf("reports = %d, want 1", reports.count())
	}
	reported := reports.exCtx[0].IP
	if reported != "203.0.113.9" {
		t.Fatalf("reported IP = %q, want the forwarded client 203.0.113.9", reported)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(startedEvents) != 1 {
		t.Fatalf("RequestStarted dispatched %d times, want 1", len(startedEvents))
	}
	if got := startedEvents[0].ClientIP; got != reported {
		t.Errorf("RequestStarted.ClientIP = %q, want the reported %q", got, reported)
	}
	if got := startedEvents[0].RemoteAddr; got != "10.1.2.3:4567" {
		t.Errorf("RequestStarted.RemoteAddr = %q, want the raw peer 10.1.2.3:4567", got)
	}
}
