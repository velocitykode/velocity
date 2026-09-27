package velocity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/trace"
)

// requestIDs are the correlation ids a handler saw on its request.
type requestIDs struct {
	requestID, traceID, spanID string
}

// captureIDs returns a handler that records the ids of its request, logs
// one line through c.Log() when logs is true, and fails with err.
func captureIDs(ids *requestIDs, logs bool, err error) router.HandlerFunc {
	return func(c *router.Context) error {
		ctx := c.Request.Context()
		ids.requestID, ids.traceID, ids.spanID = trace.GetRequestID(ctx), trace.GetTraceID(ctx), trace.GetSpanID(ctx)
		if logs {
			c.Log().Info("handling the request")
		}
		return err
	}
}

// Every framework line written while serving a request, the handler's own
// c.Log() line and the error handler's report line, carries the request_id
// and trace_id the problem+json body answers with and the report carries.
func TestRequestLogLines_CarryTheIDsOfTheResponseAndTheReport(t *testing.T) {
	a, logger, rec := newFieldApp(t)
	var ids requestIDs
	a.Router.Get("/fail", captureIDs(&ids, true, errors.New("storage offline")))

	req := httptest.NewRequest(http.MethodGet, "/fail", nil)
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	a.Router.ServeHTTP(w, req)

	var body struct {
		RequestID string `json:"request_id"`
		TraceID   string `json:"trace_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", w.Body.String(), err)
	}
	if body.RequestID == "" || body.TraceID == "" {
		t.Fatalf("body carries request_id %q and trace_id %q, want both", body.RequestID, body.TraceID)
	}
	if rec.count() != 1 {
		t.Fatalf("reports = %d, want 1", rec.count())
	}
	if got := rec.exCtx[0]; got.RequestID != body.RequestID || got.TraceID != body.TraceID {
		t.Errorf("report ids = (%q, %q), want the body's (%q, %q)", got.RequestID, got.TraceID, body.RequestID, body.TraceID)
	}

	lines := logger.snapshot()
	var sawHandler, sawReport bool
	for _, line := range lines {
		sawHandler = sawHandler || line.msg == "handling the request"
		sawReport = sawReport || line.msg == "storage offline"
		if got := line.field("request_id"); got != body.RequestID {
			t.Errorf("line %q request_id = %v, want %q (%v)", line.msg, got, body.RequestID, line.kvs)
		}
		if got := line.field("trace_id"); got != body.TraceID {
			t.Errorf("line %q trace_id = %v, want %q (%v)", line.msg, got, body.TraceID, line.kvs)
		}
	}
	if !sawHandler || !sawReport {
		t.Fatalf("lines %+v: handler line %v, report line %v, want both", lines, sawHandler, sawReport)
	}
}

// c.Log() is bound to the request's ids, its method and its route pattern.
func TestContextLog_BindsTheRequestFields(t *testing.T) {
	a, logger, _ := newFieldApp(t)
	var ids requestIDs
	a.Router.Get("/users/{id}", func(c *router.Context) error {
		if err := captureIDs(&ids, true, nil)(c); err != nil {
			return err
		}
		return c.String(http.StatusOK, "ok")
	})

	a.Router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/users/7", nil))

	var line *fieldLine
	for _, l := range logger.snapshot() {
		if l.msg == "handling the request" {
			l := l
			line = &l
		}
	}
	if line == nil {
		t.Fatal("no handler line")
	}
	for key, want := range map[string]string{
		"request_id": ids.requestID,
		"trace_id":   ids.traceID,
		"span_id":    ids.spanID,
		"method":     http.MethodGet,
		"route":      "/users/{id}",
	} {
		if got := line.field(key); got != want {
			t.Errorf("%s = %v, want %q (%v)", key, got, want, line.kvs)
		}
	}
}

// correlationKeys are the keys a failed request's line carries under the
// same name whichever path logs it.
var correlationKeys = []string{"request_id", "trace_id", "span_id", "method", "url"}

// A failed request logged by the router default, by the bridge's
// no-handler fallback and by the LogReporter carries its ids, method and
// path under the same keys, and the two fallback lines share one message.
func TestFailedRequestLines_ShareKeys(t *testing.T) {
	tests := []struct {
		name  string
		setup func(a *App)
		msg   string
	}{
		{name: "router default", setup: func(a *App) { a.Router.SetErrorHandler(nil) }, msg: "unhandled error in HTTP handler"},
		{name: "bridge without error handler", setup: func(a *App) { a.Services.Errors = nil }, msg: "unhandled error in HTTP handler"},
		{name: "log reporter", setup: func(*App) {}, msg: "disk full"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, logger, _ := newFieldApp(t)
			tt.setup(a)
			var ids requestIDs
			a.Router.Get("/fail", captureIDs(&ids, false, errors.New("disk full")))

			a.Router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/fail", nil))

			var line *fieldLine
			for _, l := range logger.snapshot() {
				if l.level == "error" && l.msg == tt.msg {
					l := l
					line = &l
				}
			}
			if line == nil {
				t.Fatalf("no error line %q in %+v", tt.msg, logger.snapshot())
			}
			want := map[string]string{
				"request_id": ids.requestID,
				"trace_id":   ids.traceID,
				"span_id":    ids.spanID,
				"method":     http.MethodGet,
				"url":        "/fail",
			}
			for _, key := range correlationKeys {
				if got := line.field(key); got != want[key] {
					t.Errorf("%s = %v, want %q (%v)", key, got, want[key], line.kvs)
				}
			}
		})
	}
}

// The warning for a request the server cut off while shutting down names
// the path under url and carries the request's ids.
func TestShutdownCutOffLine_CarriesTheRequestIDs(t *testing.T) {
	a, logger, _ := newFieldApp(t)
	var ids requestIDs
	a.Router.Get("/slow", func(c *router.Context) error {
		ctx := c.Request.Context()
		ids.requestID, ids.traceID, ids.spanID = trace.GetRequestID(ctx), trace.GetTraceID(ctx), trace.GetSpanID(ctx)
		return ctx.Err()
	})
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(contract.ErrServerShuttingDown)

	a.Router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/slow", nil).WithContext(ctx))

	var line *fieldLine
	for _, l := range logger.snapshot() {
		if l.msg == "problem: request cut off by server shutdown" {
			l := l
			line = &l
		}
	}
	if line == nil {
		t.Fatalf("no cut-off line in %+v", logger.snapshot())
	}
	for key, want := range map[string]string{"url": "/slow", "request_id": ids.requestID, "trace_id": ids.traceID, "span_id": ids.spanID} {
		if got := line.field(key); got != want {
			t.Errorf("%s = %v, want %q (%v)", key, got, want, line.kvs)
		}
	}
}
