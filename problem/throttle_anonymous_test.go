package problem_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/problem/routerbridge"
	"github.com/velocitykode/velocity/router"
)

// Every anonymous throttle rule keeps its own reporting budget.
//
// The contract lets a rule leave Key nil ("nil marks an anonymous rule"),
// AddThrottleRule keeps every anonymous rule it is given, and
// Throttle.MaxPerWindow caps reports "per Window for each bucket", where a
// nil By puts every error the rule matches in that rule's one bucket. The
// natural way to write one rule per sentinel is a loop over them, so the
// matchers are closures from one function literal. Each registration is
// still its own rule: one outage must not spend another outage's budget.

// errPaymentsDown and errSearchDown are the app's upstream outage sentinels.
var (
	errPaymentsDown = errors.New("payments gateway unavailable")
	errSearchDown   = errors.New("search cluster unavailable")
)

// configureOutageThrottles is the app's Errors callback (App.Errors hands it
// the contract.ErrorHandler): one anonymous throttle rule per outage
// sentinel, built in a loop, with its own hourly budget: one payments
// report, two search reports.
func configureOutageThrottles(h contract.ErrorHandler) {
	for _, o := range []struct {
		target error
		budget int
	}{
		{errPaymentsDown, 1},
		{errSearchDown, 2},
	} {
		h.AddThrottleRule(contract.ThrottleRule{
			Match:    func(err error) bool { return errors.Is(err, o.target) },
			Throttle: contract.Throttle{MaxPerWindow: o.budget, Window: time.Hour},
		})
	}
}

// outageReportRecorder records every report the pipeline makes.
type outageReportRecorder struct {
	mu   sync.Mutex
	errs []error
}

func (r *outageReportRecorder) record(err error, _ *problem.ErrorContext) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, err)
}

func (r *outageReportRecorder) reported() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]error(nil), r.errs...)
}

// describeReports quotes each reported error's message.
func describeReports(errs []error) string {
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		parts = append(parts, fmt.Sprintf("%q", err.Error()))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// outageRouterApp is a router app wired as velocity.New wires it: the
// handler is Services.Errors and the router boundary reaches it through
// routerbridge. Each route fails with its outage.
func outageRouterApp(h *problem.Handler) http.Handler {
	r := router.New()
	r.SetServices(&app.Services{Errors: h})
	routerbridge.Install(r, routerbridge.WithHandler(func() contract.ErrorHandler { return h }))
	r.Get("/payments/charge", func(*router.Context) error {
		return fmt.Errorf("charge card: %w", errPaymentsDown)
	})
	r.Get("/search", func(*router.Context) error {
		return fmt.Errorf("query index: %w", errSearchDown)
	})
	return r
}

// outageNetHTTPApp is a net/http app: problem.Middleware wraps the mux, and
// a handler that fails hands its error to problem.ErrorHandler.
func outageNetHTTPApp(h *problem.Handler) http.Handler {
	onError := problem.ErrorHandler(h)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /payments/charge", func(w http.ResponseWriter, r *http.Request) {
		onError(w, r, fmt.Errorf("charge card: %w", errPaymentsDown))
	})
	mux.HandleFunc("GET /search", func(w http.ResponseWriter, r *http.Request) {
		onError(w, r, fmt.Errorf("query index: %w", errSearchDown))
	})
	return problem.Middleware(h)(mux)
}

// TestAddThrottleRule_AnonymousRulesKeepOwnBudgets asserts two anonymous
// throttle rules whose matchers come from one function literal (capturing
// different sentinels) each spend only their own budget, through the
// router boundary and through the net/http ErrorHandler.
func TestAddThrottleRule_AnonymousRulesKeepOwnBudgets(t *testing.T) {
	tests := []struct {
		name  string
		serve func(h *problem.Handler) http.Handler
	}{
		{name: "RouterBoundary", serve: outageRouterApp},
		{name: "NetHTTPErrorHandler", serve: outageNetHTTPApp},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &outageReportRecorder{}
			h := problem.NewHandler(problem.WithReporters(problem.NewCallbackReporter(rec.record)))
			configureOutageThrottles(h)
			srv := tt.serve(h)

			// Every request falls inside one hour. Each rule reports up to
			// its own budget and throttles past it.
			steps := []struct {
				path   string
				want   error // the outage this request reports; nil when throttled
				reason string
			}{
				{"/payments/charge", errPaymentsDown, "the first payments outage this hour is reported"},
				{"/search", errSearchDown, "the first search outage this hour is reported: the search rule has its own budget and has reported nothing yet"},
				{"/payments/charge", nil, "the payments rule already spent its one report this hour"},
				{"/search", errSearchDown, "the second search outage this hour is reported: the search budget is two"},
				{"/search", nil, "the search rule already spent its two reports this hour"},
			}
			wantReports := 0
			for i, st := range steps {
				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, st.path, nil)
				req.Header.Set("Accept", "application/json")
				srv.ServeHTTP(w, req)

				// Throttling decides only the report: every outage still
				// answers 500.
				if w.Code != http.StatusInternalServerError {
					t.Fatalf("step %d GET %s: status = %d, want 500 (body %q)", i+1, st.path, w.Code, w.Body.String())
				}
				if st.want != nil {
					wantReports++
				}
				got := rec.reported()
				if len(got) != wantReports {
					t.Fatalf("step %d GET %s: reports = %d, want %d (%s); reported so far %s",
						i+1, st.path, len(got), wantReports, st.reason, describeReports(got))
				}
				if st.want != nil && !errors.Is(got[len(got)-1], st.want) {
					t.Fatalf("step %d GET %s: reported %q, want the %q outage", i+1, st.path, got[len(got)-1], st.want)
				}
			}
		})
	}
}
