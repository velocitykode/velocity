package problem

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/hostile"
)

// hostileWriter is a ResponseWriter whose WriteHeader and Write run code
// first.
type hostileWriter struct {
	*httptest.ResponseRecorder
	code *hostile.Code
}

func (w *hostileWriter) WriteHeader(status int) { w.code.Run(); w.ResponseRecorder.WriteHeader(status) }
func (w *hostileWriter) Write(b []byte) (int, error) {
	w.code.Run()
	return w.ResponseRecorder.Write(b)
}

// hostileReporter runs code, then records nothing.
type hostileReporter struct{ code *hostile.Code }

func (r hostileReporter) Report(error, *ErrorContext) { r.code.Run() }

// recoveredReports counts the reports rep got for a recovered panic.
func recoveredReports(rep *recReporter) int {
	rep.mu.Lock()
	defer rep.mu.Unlock()
	n := 0
	for _, ctx := range rep.ctxs {
		if ctx != nil && ctx.Recovered {
			n++
		}
	}
	return n
}

// The pipeline against hostile user code, one row per stage kind (the map
// rule's Match and Map, a render rule, a report rule, a reporter, a
// context provider, a BeforeRender hook and the ResponseWriter), in the
// modes that apply: panic, and re-entry into the handler for another
// request. Block does not apply: the pipeline reads a snapshot of its
// rules and holds no lock while user code runs. Every row asserts that no
// panic leaves HandleRequest, that the request is still answered (for a
// hostile writer, that the last resort was tried), and that a panic is
// recorded once, through the recover of the stage it happened in.
func TestHandler_HostileStageSweep(t *testing.T) {
	type row struct {
		name    string
		install func(h *Handler, code *hostile.Code)
		writer  bool
		// panicked reports how many times the stage's recover recorded
		// the panic: its log line, or a recovered-panic report.
		panicked func(logger *recLogger, rep *recReporter) int
	}
	logged := func(msg string) func(*recLogger, *recReporter) int {
		return func(logger *recLogger, _ *recReporter) int {
			n := 0
			for _, e := range logger.all() {
				if e.level == "error" && e.msg == msg {
					n++
				}
			}
			return n
		}
	}
	reported := func(_ *recLogger, rep *recReporter) int { return recoveredReports(rep) }
	always := func(error) bool { return true }
	rows := []row{
		{"map rule Match", func(h *Handler, c *hostile.Code) {
			h.AddMapRule(contract.MapRule{Match: func(error) bool { c.Run(); return false }, Map: func(err error) error { return err }})
		}, false, logged("problem: map rule panicked")},
		{"map rule Map", func(h *Handler, c *hostile.Code) {
			h.AddMapRule(contract.MapRule{Match: always, Map: func(err error) error { c.Run(); return err }})
		}, false, logged("problem: map rule panicked")},
		{"render rule", func(h *Handler, c *hostile.Code) {
			h.AddRenderRule(contract.RenderRule{Match: always, Render: func(contract.RenderContext, error, *contract.ErrorContext) bool { c.Run(); return false }})
		}, false, reported},
		{"report rule", func(h *Handler, c *hostile.Code) {
			h.AddReportRule(contract.ReportRule{Match: always, Report: func(error, *contract.ErrorContext) bool { c.Run(); return false }})
		}, false, logged("problem: report failed")},
		{"reporter", func(h *Handler, c *hostile.Code) {
			h.AddReporter(hostileReporter{c})
		}, false, logged("problem: reporter panicked")},
		{"context provider", func(h *Handler, c *hostile.Code) {
			h.ContextUsing(func(error, *ErrorContext) map[string]any { c.Run(); return nil })
		}, false, logged("problem: report failed")},
		{"BeforeRender hook", func(h *Handler, c *hostile.Code) {
			h.BeforeRender(func(_ RenderContext, _ error, status int) int { c.Run(); return status })
		}, false, reported},
		{"ResponseWriter", func(*Handler, *hostile.Code) {}, true, reported},
	}
	for _, mode := range []hostile.Mode{hostile.Panic, hostile.Reenter} {
		for _, r := range rows {
			t.Run(mode.String()+"/"+r.name, func(t *testing.T) {
				h, rep, logger := newTestHandler()
				nested := httptest.NewRecorder()
				code := hostile.New(t, mode, func() {
					req := httptest.NewRequest(http.MethodGet, "/nested", nil)
					h.HandleRequest(contract.NewRenderContext(nested, req), errors.New("nested failure"), nil)
				})
				r.install(h, code)
				rec := httptest.NewRecorder()
				var w http.ResponseWriter = rec
				if r.writer {
					w = &hostileWriter{ResponseRecorder: rec, code: code}
				}
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				if p := hostile.Within(t, hostile.Deadline, func() {
					h.HandleRequest(contract.NewRenderContext(w, req), errors.New("boom"), nil)
				}); p != nil {
					t.Fatalf("a panic left HandleRequest: %v", p)
				}
				switch {
				case r.writer && mode == hostile.Panic:
					// The writer itself panics: nothing can be written, and
					// the last resort's attempt is logged.
					if !logger.has("error", "problem: last-resort response failed") {
						t.Error("the last resort was not tried")
					}
				case rec.Code != http.StatusInternalServerError || rec.Body.Len() == 0:
					t.Errorf("response = %d %q, want a 500 with a body", rec.Code, rec.Body.String())
				}
				want := 0
				if mode == hostile.Panic {
					want = 1
				}
				if got := r.panicked(logger, rep); got != want {
					t.Errorf("panics recorded = %d, want %d", got, want)
				}
				if mode == hostile.Reenter && (nested.Code != http.StatusInternalServerError || nested.Body.Len() == 0) {
					t.Errorf("nested response = %d %q, want a 500 with a body", nested.Code, nested.Body.String())
				}
			})
		}
	}
}
