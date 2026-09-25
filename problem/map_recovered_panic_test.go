package problem_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/problem/routerbridge"
	"github.com/velocitykode/velocity/router"
)

// A recovered panic is a reported 500 whatever its value, and whatever a
// user map rule returns for it. MapIs and MapFor reach the panic value
// through Unwrap, so a rule for an app sentinel matches a panic whose value
// is that sentinel and may return an error that no longer carries the
// panic. The pipeline decides that the error is a recovered panic before
// the rule runs, so the report gate and the render stage still see one.
//
// These tests drive the two framework call sites that hand the pipeline a
// panic without flagging ctx.Recovered themselves:
//   - problem.ErrorHandler, the net/http adapter for handlers that return
//     their errors, calls HandleRequest with a nil ctx.
//   - router.Context.Report calls Services.Errors.Report with a ctx that
//     does not flag it, and returns the error marked reported, so the
//     router boundary renders the 500 without reporting it again.
//
// The panic reaches both the way an app meets one as a value: work run
// through async.Run panics, and Get returns the recovered panic as an
// error. Each call site runs once without the map rule and once with it.

// errInvoiceMissing is the app's lookup sentinel, mapped to a 404.
var errInvoiceMissing = errors.New("invoice missing")

// mustFindInvoice is a Must-style lookup that panics with the lookup's
// error: reaching it for a missing invoice is a bug, whatever the value.
func mustFindInvoice() int {
	panic(errInvoiceMissing)
}

// panicReportRecorder records every report the pipeline makes.
type panicReportRecorder struct {
	mu   sync.Mutex
	errs []error
	ctxs []*problem.ErrorContext
}

func (r *panicReportRecorder) record(err error, ctx *problem.ErrorContext) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, err)
	r.ctxs = append(r.ctxs, ctx)
}

func (r *panicReportRecorder) reports() ([]error, []*problem.ErrorContext) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]error(nil), r.errs...), append([]*problem.ErrorContext(nil), r.ctxs...)
}

// quietAsyncLogger drops the async package's own panic log line (a stack
// trace) so the test output shows the assertions.
type quietAsyncLogger struct{}

func (quietAsyncLogger) Error(string, ...any) {}

// getInvoice serves one JSON GET /invoices/7 through h.
func getInvoice(h http.Handler) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/invoices/7", nil)
	req.Header.Set("Accept", "application/json")
	h.ServeHTTP(w, req)
	return w
}

func TestMapRule_RecoveredPanicStaysReported500(t *testing.T) {
	prev := async.GetLogger()
	async.SetLogger(quietAsyncLogger{})
	t.Cleanup(func() { async.SetLogger(prev) })

	// A net/http app: problem.Middleware recovers the handlers' panics,
	// and a handler that fails hands its error to problem.ErrorHandler.
	netHTTPApp := func(h *problem.Handler, seen *error) *httptest.ResponseRecorder {
		onError := problem.ErrorHandler(h)
		mux := http.NewServeMux()
		mux.HandleFunc("GET /invoices/7", func(w http.ResponseWriter, r *http.Request) {
			if _, err := async.Run(mustFindInvoice).Get(); err != nil {
				*seen = err
				onError(w, r, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
		return getInvoice(problem.Middleware(h)(mux))
	}

	// A router app wired as velocity.New wires it: the handler is
	// Services.Errors and the router boundary reaches it through
	// routerbridge. The route reports its failure itself and returns the
	// marked error, so the boundary renders it without a second report.
	routerApp := func(h *problem.Handler, seen *error) *httptest.ResponseRecorder {
		r := router.New()
		r.SetServices(&app.Services{Errors: h})
		routerbridge.Install(r, routerbridge.WithHandler(func() contract.ErrorHandler { return h }))
		r.Get("/invoices/7", func(c *router.Context) error {
			if _, err := async.Run(mustFindInvoice).Get(); err != nil {
				*seen = err
				return c.Report(err)
			}
			return c.NoContent()
		})
		return getInvoice(r)
	}

	tests := []struct {
		name    string
		serve   func(h *problem.Handler, seen *error) *httptest.ResponseRecorder
		mapRule bool
	}{
		{name: "ErrorHandlerAdapter_NoMapRule", serve: netHTTPApp},
		{name: "ErrorHandlerAdapter_MapRuleDropsThePanic", serve: netHTTPApp, mapRule: true},
		{name: "ContextReport_NoMapRule", serve: routerApp},
		{name: "ContextReport_MapRuleDropsThePanic", serve: routerApp, mapRule: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &panicReportRecorder{}
			h := problem.NewHandler(problem.WithReporters(problem.NewCallbackReporter(rec.record)))
			if tt.mapRule {
				// The app maps its lookup sentinel to a 404. MapIs matches
				// the panic through its Unwrap, and the replacement does
				// not keep the error it replaces.
				problem.MapIs(h, errInvoiceMissing, func(error) error {
					return problem.NotFound("invoice not found")
				})
			}

			var seen error
			w := tt.serve(h, &seen)

			// Precondition: the route failed with a recovered panic whose
			// value is the mapped sentinel.
			var rp contract.RecoveredPanic
			if !errors.As(seen, &rp) || !errors.Is(seen, errInvoiceMissing) {
				t.Fatalf("route error = %v, want a recovered panic of errInvoiceMissing", seen)
			}

			// A recovered panic is a reported 500 whatever its value.
			if w.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500 for a recovered panic (body %q)", w.Code, w.Body.String())
			}
			errs, ctxs := rec.reports()
			if len(errs) != 1 {
				t.Fatalf("reports = %d, want exactly 1 for a recovered panic", len(errs))
			}
			if !ctxs[0].Recovered {
				t.Errorf("reported ctx.Recovered = false, want true (reported %v)", errs[0])
			}
		})
	}
}
