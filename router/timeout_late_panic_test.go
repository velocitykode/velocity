package router_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/problem/routerbridge"
	"github.com/velocitykode/velocity/router"
)

// latePanicValue is what a handler panics with once its cleanup, which
// outlives the Timeout middleware's 503, resumes.
const latePanicValue = "cleanup panicked after the timeout answered"

// latePanicObserver records what one router did with a request: every
// report the problem pipeline made, every error and warn line the
// standalone router logged, and every RequestFailed event it dispatched.
type latePanicObserver struct {
	mu      sync.Mutex
	reports []latePanicSignal
	errors  []string
	warns   []string
	failed  []latePanicSignal
}

// latePanicSignal is one report or RequestFailed event: the error text it
// carried and whether it was flagged as a recovered panic.
type latePanicSignal struct {
	err       string
	recovered bool
}

func (o *latePanicObserver) report(err error, ctx *problem.ErrorContext) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.reports = append(o.reports, latePanicSignal{err: err.Error(), recovered: ctx != nil && ctx.Recovered})
}

func (o *latePanicObserver) errorLine(msg string, kvs ...any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.errors = append(o.errors, msg+" "+fmt.Sprint(kvs...))
}

func (o *latePanicObserver) warnLine(msg string, kvs ...any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.warns = append(o.warns, msg+" "+fmt.Sprint(kvs...))
}

func (o *latePanicObserver) dispatch(_ context.Context, event interface{}) error {
	rf, ok := event.(*router.RequestFailed)
	if !ok {
		return nil
	}
	s := latePanicSignal{recovered: rf.Recovered}
	if rf.Error != nil {
		s.err = rf.Error.Error()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.failed = append(o.failed, s)
	return nil
}

// panics returns the pipeline reports and error lines that carry the
// panic value, and the RequestFailed events flagged Recovered (the panic
// is the request's only one).
func (o *latePanicObserver) panics() (reports []latePanicSignal, lines []string, failed []latePanicSignal) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, r := range o.reports {
		if strings.Contains(r.err, latePanicValue) {
			reports = append(reports, r)
		}
	}
	for _, l := range o.errors {
		if strings.Contains(l, latePanicValue) {
			lines = append(lines, l)
		}
	}
	for _, f := range o.failed {
		if f.recovered {
			failed = append(failed, f)
		}
	}
	return reports, lines, failed
}

// String lists everything the observer holds, for failure messages.
func (o *latePanicObserver) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return fmt.Sprintf("pipeline reports=%+v, router error lines=%q, router warn lines=%d, RequestFailed=%+v",
		o.reports, o.errors, len(o.warns), o.failed)
}

// latePanicRouter builds a router with Timeout(50ms) as its first global
// middleware, observed by obs, in one of two modes: "pipeline" (the
// problem pipeline installed with routerbridge, as velocity.New wires it)
// or "standalone" (the router's own error and warn loggers).
func latePanicRouter(mode string, obs *latePanicObserver) *router.VelocityRouterV2 {
	r := router.New()
	r.SetEventDispatcher(obs.dispatch)
	if mode == "pipeline" {
		h := problem.NewHandler(problem.WithReporters(problem.NewCallbackReporter(obs.report)))
		h.SetDebug(false)
		routerbridge.Install(r, routerbridge.WithHandler(func() contract.ErrorHandler { return h }))
	} else {
		r.SetErrorLogger(obs.errorLine)
		r.SetWarnLogger(obs.warnLine)
	}
	r.Use(router.Timeout(50 * time.Millisecond))
	return r
}

// TestTimeout_PanicAfterTimeoutAnsweredIsReported asserts that a panic the
// Timeout middleware's handler goroutine recovers after the middleware
// already answered 503 is still reported, once, like every other recovered
// panic. The handler honours the deadline, but its cleanup runs on past
// the 503 and panics only after ServeHTTP has returned. Through the
// problem pipeline the reporter receives that panic once with
// ErrorContext.Recovered set; on the standalone router the error logger
// logs it once; either way the router dispatches one RequestFailed with
// Recovered set for it. The client keeps its 503: the late panic writes
// nothing. The panic may come from the matched route's handler or, for a
// request no route matches and for a static file, from global middleware
// running inside Timeout.
func TestTimeout_PanicAfterTimeoutAnsweredIsReported(t *testing.T) {
	for _, mode := range []string{"pipeline", "standalone"} {
		for _, where := range []string{"matched route", "unmatched request", "static file"} {
			t.Run(mode+"/"+where, func(t *testing.T) {
				obs := &latePanicObserver{}
				r := latePanicRouter(mode, obs)

				release := make(chan struct{})
				exited := make(chan struct{})
				var panicked atomic.Bool
				slowThenPanic := func(c *router.Context) {
					defer close(exited)
					<-c.Request.Context().Done() // the handler honours the deadline
					<-release                    // but its cleanup outlives the 503
					panicked.Store(true)
					panic(latePanicValue)
				}
				path := "/slow"
				if where == "matched route" {
					r.Get(path, func(c *router.Context) error {
						slowThenPanic(c)
						return nil
					})
				} else {
					path = "/nowhere"
					if where == "static file" {
						dir := t.TempDir()
						if err := os.WriteFile(filepath.Join(dir, "app.css"), []byte("body{}"), 0o600); err != nil {
							t.Fatal(err)
						}
						r.Static(dir)
						path = "/app.css"
					}
					r.Use(func(next router.HandlerFunc) router.HandlerFunc {
						return func(c *router.Context) error {
							if c.Request.URL.Path == path {
								slowThenPanic(c)
							}
							return next(c)
						}
					})
				}

				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.Header.Set("Accept", "application/json")
				r.ServeHTTP(w, req)

				// ServeHTTP has returned: the client has its 503 and the
				// handler has not panicked yet.
				if w.Code != http.StatusServiceUnavailable {
					t.Fatalf("status = %d, want 503 (body %q)", w.Code, w.Body.String())
				}
				answered := w.Body.String()
				if reports, lines, failed := obs.panics(); len(reports)+len(lines)+len(failed) != 0 {
					t.Fatalf("late panic observed before the handler was released: %s", obs)
				}

				// Release the handler only now that ServeHTTP has returned.
				close(release)
				select {
				case <-exited:
				case <-time.After(5 * time.Second):
					t.Fatal("late handler never finished")
				}
				if !panicked.Load() {
					t.Fatal("late handler finished without reaching its panic")
				}

				// The report is made on the handler goroutine: wait for
				// it, then a moment more so a second report would show.
				reported := func() bool {
					reports, lines, failed := obs.panics()
					return len(reports)+len(lines) > 0 && len(failed) > 0
				}
				for deadline := time.Now().Add(2 * time.Second); !reported() && time.Now().Before(deadline); {
					time.Sleep(10 * time.Millisecond)
				}
				time.Sleep(50 * time.Millisecond)

				reports, lines, failed := obs.panics()
				if mode == "pipeline" {
					if len(reports) != 1 {
						t.Errorf("pipeline reports of the late panic = %d, want 1: a recovered panic is always reported (%s)", len(reports), obs)
					} else if !reports[0].recovered {
						t.Errorf("late panic reported without ErrorContext.Recovered: %+v", reports[0])
					}
				} else if len(lines) != 1 {
					t.Errorf("router error lines for the late panic = %d, want 1: the default path logs every recovered panic (%s)", len(lines), obs)
				}
				if len(failed) != 1 {
					t.Errorf("RequestFailed events with Recovered set = %d, want 1 for the late panic (%s)", len(failed), obs)
				} else if !strings.Contains(failed[0].err, latePanicValue) {
					t.Errorf("recovered RequestFailed carries %q, want the late panic", failed[0].err)
				}
				if w.Code != http.StatusServiceUnavailable || w.Body.String() != answered {
					t.Errorf("late panic changed the answered response: %d %q, want 503 %q", w.Code, w.Body.String(), answered)
				}
			})
		}
	}
}

// TestTimeout_PanicBeforeDeadlineReportedOnce asserts a handler panic
// under Timeout that happens before the deadline is answered 500 and
// reported exactly once, by the boundary, and not a second time by the
// goroutine's late-report path.
func TestTimeout_PanicBeforeDeadlineReportedOnce(t *testing.T) {
	for _, mode := range []string{"pipeline", "standalone"} {
		t.Run(mode, func(t *testing.T) {
			obs := &latePanicObserver{}
			r := latePanicRouter(mode, obs)
			r.Get("/boom", func(*router.Context) error { panic(latePanicValue) })

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/boom", nil)
			req.Header.Set("Accept", "application/json")
			r.ServeHTTP(w, req)
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500 (body %q)", w.Code, w.Body.String())
			}
			// Give a stray second report time to show.
			time.Sleep(100 * time.Millisecond)

			reports, lines, failed := obs.panics()
			if got := len(reports) + len(lines); got != 1 {
				t.Errorf("reports and error lines of the panic = %d, want 1 (%s)", got, obs)
			}
			if len(failed) != 1 {
				t.Errorf("RequestFailed events with Recovered set = %d, want 1 (%s)", len(failed), obs)
			}
		})
	}
}

// TestTimeout_LateAbortReportsNothing asserts a handler that panics with
// http.ErrAbortHandler after Timeout answered 503 is dropped: there is no
// connection left to abort, and the sentinel is never a reported failure.
func TestTimeout_LateAbortReportsNothing(t *testing.T) {
	for _, mode := range []string{"pipeline", "standalone"} {
		t.Run(mode, func(t *testing.T) {
			obs := &latePanicObserver{}
			r := latePanicRouter(mode, obs)
			release := make(chan struct{})
			exited := make(chan struct{})
			r.Get("/slow", func(c *router.Context) error {
				defer close(exited)
				<-c.Request.Context().Done()
				<-release
				panic(http.ErrAbortHandler)
			})

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/slow", nil)
			req.Header.Set("Accept", "application/json")
			r.ServeHTTP(w, req)
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503 (body %q)", w.Code, w.Body.String())
			}
			before := obs.String()
			close(release)
			select {
			case <-exited:
			case <-time.After(5 * time.Second):
				t.Fatal("late handler never finished")
			}
			time.Sleep(100 * time.Millisecond)

			if after := obs.String(); after != before {
				t.Errorf("late abort was observed: before %s, after %s", before, after)
			}
			if _, _, failed := obs.panics(); len(failed) != 0 {
				t.Errorf("RequestFailed events with Recovered set = %d, want 0 (%s)", len(failed), obs)
			}
		})
	}
}
