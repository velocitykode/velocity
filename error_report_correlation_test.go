package velocity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/console"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/queue"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/trace"
)

// newFieldApp builds a test app whose Services.Log, installed through a log
// driver so the error handler's LogReporter writes to it too, is the
// returned fieldLogger, with a recording reporter on the error handler.
// Boot-time lines are cleared before it returns.
func newFieldApp(t *testing.T) (*App, *fieldLogger, *recordingReporter) {
	t.Helper()
	logger := newFieldLogger()
	const driverName = "field-capture"
	prev := log.Drivers().Override(driverName, func(context.Context, log.LogConfig) (log.Logger, error) {
		return logger, nil
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
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	rec := &recordingReporter{}
	a.Services.Errors.AddReporter(rec)
	logger.reset()
	return a, logger, rec
}

// userAuth is an auth manager whose facet names every request's user.
type userAuth struct {
	// contract.AuthManager supplies the methods this fake does not use.
	contract.AuthManager
	id string
}

func (userAuth) Allows(*http.Request, string, ...interface{}) bool     { return true }
func (userAuth) Authorize(*http.Request, string, ...interface{}) error { return nil }
func (u userAuth) RequestUserID(*http.Request) string                  { return u.id }

// c.Report names the request's user, as the router boundary's report does.
func TestContextReport_CarriesTheUser(t *testing.T) {
	a, _, rec := newFieldApp(t)
	a.Services.Auth = userAuth{id: "user-42"}
	a.Router.Get("/report", func(c *router.Context) error {
		_ = c.Report(errors.New("quota warning"))
		return c.String(http.StatusOK, "ok")
	})

	a.Router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/report", nil))

	if rec.count() != 1 {
		t.Fatalf("reports = %d, want 1", rec.count())
	}
	if got := rec.exCtx[0].UserID; got != "user-42" {
		t.Errorf("UserID = %q, want %q", got, "user-42")
	}
}

// The failure bridge reports a background failure with the request, trace
// and span ids of the context it was dispatched under.
func TestFailureBridgeReport_CarriesTheSpanAndRequestIDs(t *testing.T) {
	rec := &recordingReporter{}
	h := problem.NewHandler(problem.WithReporters(rec))
	ctx := trace.WithRequestID(trace.WithTrace(context.Background(), "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"), "req-1")

	buildFailureReporter(h)(ctx, &queue.JobFailed{JobType: "SendMail", Queue: "default"}, errors.New("smtp down"))

	if rec.count() != 1 {
		t.Fatalf("reports = %d, want 1", rec.count())
	}
	got := rec.exCtx[0]
	if got.RequestID != "req-1" || got.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || got.SpanID != "00f067aa0ba902b7" {
		t.Errorf("ids = (%q, %q, %q), want (req-1, the trace, the span)", got.RequestID, got.TraceID, got.SpanID)
	}
}

// traceRecordingListener is a queued listener that fails every run and
// records the trace and span ids of the context each run gets.
type traceRecordingListener struct {
	mu              sync.Mutex
	traceID, spanID string
}

func (l *traceRecordingListener) Handle(ctx context.Context, _ interface{}) error {
	l.mu.Lock()
	l.traceID, l.spanID = trace.GetTraceID(ctx), trace.GetSpanID(ctx)
	l.mu.Unlock()
	return errors.New("queued listener exploded")
}

func (*traceRecordingListener) Async() bool { return true }

func (l *traceRecordingListener) ids() (string, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.traceID, l.spanID
}

// A queued listener the worker fails permanently is reported, through the
// listener's own Failed hook, with the trace and span its run had and the
// job type under the key the worker's lines use.
func TestQueuedListenerReport_CarriesTheJobTrace(t *testing.T) {
	for name, newQueue := range map[string]func(*testing.T, *App) workerQueue{
		"memory":   memoryQueue,
		"redis":    redisQueue,
		"database": databaseQueue,
	} {
		t.Run(name, func(t *testing.T) {
			app, err := NewTestApp()
			if err != nil {
				t.Fatalf("NewTestApp: %v", err)
			}
			defer app.Shutdown(context.Background())
			reports := &failureReports{}
			reports.add(app)
			q := newQueue(t, app)
			listener := &traceRecordingListener{}
			job := queuedListenerJob(t, "velocity.traceRecordingListener", listener)

			opts := queueWorkOptions(app, console.QueueWorkOptions{Tries: 1})
			opts.Dispatcher = nil
			runStockWorker(t, q.driver, opts, job, q.failedRecorded)

			if reports.count() != 1 {
				t.Fatalf("reports = %d, want 1", reports.count())
			}
			traceID, spanID := listener.ids()
			if traceID == "" || spanID == "" {
				t.Fatalf("listener ran without a trace (%q, %q)", traceID, spanID)
			}
			got := reports.ctxs[0]
			if got.TraceID != traceID || got.SpanID != spanID {
				t.Errorf("report ids = (%q, %q), want the run's (%q, %q)", got.TraceID, got.SpanID, traceID, spanID)
			}
			if jt, _ := got.Extra["job_type"].(string); jt != "EventListenerJob" {
				t.Errorf("Extra[job_type] = %q, want EventListenerJob (extra %v)", jt, got.Extra)
			}
		})
	}
}

var _ events.Listener = (*traceRecordingListener)(nil)
