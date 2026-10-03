package velocity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/router"
)

// A request the app's router refuses once its Shutdown began is answered
// 503 with Retry-After and Connection: close through the framework error
// pipeline, and neither reported nor logged: the refusal is an outcome of
// the shutdown, not a failure.
func TestErrorPipeline_RouterRefusalIsAnUnreported503(t *testing.T) {
	a, logs, rec := newPipelineApp(t)
	ran := false
	a.Router.Get("/", func(c *router.Context) error { ran = true; return c.NoContent() })
	if err := a.Router.Shutdown(context.Background()); err != nil {
		t.Fatalf("Router.Shutdown: %v", err)
	}
	w := httptest.NewRecorder()
	a.Router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	if got := w.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q, want 1", got)
	}
	if got := w.Header().Get("Connection"); got != "close" {
		t.Errorf("Connection = %q, want close", got)
	}
	if ran {
		t.Error("the handler ran for a refused request")
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.errs) != 0 {
		t.Errorf("reports = %v, want none", rec.errs)
	}
	if n := logs.count("error") + logs.count("warn"); n != 0 {
		t.Errorf("error/warn log entries = %d, want 0", n)
	}
}
