package router

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/hostile"
)

// reportingWrapper is an application writer between two of the router's
// writers that reports the commitment of the writer it wraps.
type reportingWrapper struct{ http.ResponseWriter }

func (w *reportingWrapper) Committed() bool {
	return w.ResponseWriter.(contract.CommitReporter).Committed()
}

// One request has one writer that records its commitment; a router's writer
// stacked on it asks it. The error handler renders through a second writer
// of the router's over the request's own, keeping a render adapter of the
// inner context. A listener of the request's writer writes a status through
// that adapter, which the request's writer refuses, and panics: no layer
// may believe the refused write, so the render falls back to its 500 and
// the listener still pending sees the 500 the client gets.
func TestBeforeCommit_StackedOwnersAskTheOneThatRecords(t *testing.T) {
	stacks := map[string]func(c *Context, h HandlerFunc){
		"owner over owner, Wrap": func(c *Context, h HandlerFunc) {
			Wrap(h)(c.Response, c.Request)
		},
		"owner over reporting wrapper over owner, Wrap": func(c *Context, h HandlerFunc) {
			Wrap(h)(&reportingWrapper{c.Response}, c.Request)
		},
		"owner over owner, nested router": func(c *Context, h HandlerFunc) {
			inner := New()
			inner.Get("/x", h)
			inner.ServeHTTP(c.Response, c.Request)
		},
		"owner over reporting wrapper over owner, nested router": func(c *Context, h HandlerFunc) {
			inner := New()
			inner.Get("/x", h)
			inner.ServeHTTP(&reportingWrapper{c.Response}, c.Request)
		},
		"owner over reporting wrapper over owner, bound on demand": func(c *Context, h HandlerFunc) {
			inner := NewContext(&reportingWrapper{c.Response}, c.Request)
			inner.BeforeCommit(func(int, http.ResponseWriter) {})
			_ = h(inner)
		},
	}
	adapters := map[string]func(c *Context) contract.RenderContext{
		"the Context's render adapter": func(c *Context) contract.RenderContext { return c.RenderContext() },
		"a render adapter over the writer": func(c *Context) contract.RenderContext {
			return contract.NewRenderContext(c.Response, c.Request)
		},
	}
	for stackName, stack := range stacks {
		for adapterName, adapter := range adapters {
			t.Run(stackName+"/"+adapterName, func(t *testing.T) {
				var tail []int
				var innerSaid []bool
				r := New()
				r.SetErrorHandler(func(c *Context, _ error, _ ErrorInfo) {
					var rc contract.RenderContext
					var inner *Context
					c.BeforeCommit(func(status int, _ http.ResponseWriter) { tail = append(tail, status) })
					c.BeforeCommit(func(int, http.ResponseWriter) {
						rc.WriteHeader(http.StatusNoContent)
						innerSaid = append(innerSaid, inner.Response.(contract.CommitReporter).Committed())
						panic("listener exploded")
					})
					stack(c, func(in *Context) error {
						inner = in
						rc = adapter(in)
						func() {
							defer func() {
								if recover() != nil && !rc.Written() {
									rc.WriteHeader(http.StatusInternalServerError)
								}
							}()
							rc.WriteHeader(http.StatusNotFound)
						}()
						return nil
					})
				})
				r.Get("/x", func(*Context) error { return errors.New("failed") })
				rec := httptest.NewRecorder()
				if p := hostile.Within(t, hostile.Deadline, func() {
					r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
				}); p != nil {
					t.Fatalf("panicked: %v", p)
				}
				if !reflect.DeepEqual(innerSaid, []bool{false}) {
					t.Fatalf("the inner writer reported committed = %v after a write the request's writer refused, want [false]", innerSaid)
				}
				if rec.Code != http.StatusInternalServerError {
					t.Fatalf("status = %d, want 500", rec.Code)
				}
				if !reflect.DeepEqual(tail, []int{500}) {
					t.Fatalf("the pending listener saw %v, want [500]", tail)
				}
			})
		}
	}
}

// A router's writer over one that reports its commitment says what that one
// says, for a commitment made past it as well, and records a status only
// when the write was taken.
func TestResponseWriter_AnswersFromTheWriterItStandsOn(t *testing.T) {
	rec := httptest.NewRecorder()
	lower := &responseWriter{status: http.StatusOK}
	lower.bind(rec)
	upper := &responseWriter{status: http.StatusOK}
	upper.bind(lower)

	lower.addListener(func(int, http.ResponseWriter) {
		upper.WriteHeader(http.StatusNoContent)
		if n, err := upper.Write([]byte("x")); n != 0 || err == nil {
			t.Errorf("Write during the lower writer's dispatch = %d, %v; want 0 and an error", n, err)
		}
		upper.Flush()
		if upper.Committed() || upper.Status() != http.StatusOK {
			t.Errorf("after refused writes: Committed = %v, Status = %d; want false, 200", upper.Committed(), upper.Status())
		}
	})
	upper.WriteHeader(http.StatusAccepted)
	if !upper.Committed() || upper.Status() != http.StatusAccepted || rec.Code != http.StatusAccepted {
		t.Fatalf("after a taken write: Committed = %v, Status = %d, sent %d; want true, 202, 202", upper.Committed(), upper.Status(), rec.Code)
	}

	rec = httptest.NewRecorder()
	lower = &responseWriter{status: http.StatusOK}
	lower.bind(rec)
	upper = &responseWriter{status: http.StatusOK}
	upper.bind(lower)
	lower.WriteHeader(http.StatusTeapot)
	if !upper.Committed() {
		t.Fatal("Committed = false although the writer underneath committed")
	}
	if upper.addListener(func(int, http.ResponseWriter) {}) {
		t.Fatal("a listener registered on a response the writer underneath had committed")
	}
	upper.WriteHeader(http.StatusOK)
	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want the 418 already sent", rec.Code)
	}
}

// silentWrapper is an application writer that does not report commitment.
type silentWrapper struct{ http.ResponseWriter }

// EXCLUDED BEHAVIOUR, pinned as it is today, not as it should be. A writer
// that does not implement contract.CommitReporter sits between two of the
// router's writers: the upper one cannot ask, so it takes a write the
// request's writer refused as sent, the render skips its fallback, and the
// client gets an empty implicit 200 where the stacks above get a 500. The
// godoc of Context.BeforeCommit and Wrap says such a writer must report.
// If the router ever learns to see through such a writer, this test fails:
// flip it to the expectations of
// TestBeforeCommit_StackedOwnersAskTheOneThatRecords.
func TestBeforeCommit_NonReportingWriterBetweenOwners_ExcludedBehaviour(t *testing.T) {
	var tail []int
	var innerSaid []bool
	r := New()
	r.SetErrorHandler(func(c *Context, _ error, _ ErrorInfo) {
		var rc contract.RenderContext
		var inner *Context
		c.BeforeCommit(func(status int, _ http.ResponseWriter) { tail = append(tail, status) })
		c.BeforeCommit(func(int, http.ResponseWriter) {
			rc.WriteHeader(http.StatusNoContent)
			innerSaid = append(innerSaid, inner.Response.(contract.CommitReporter).Committed())
			panic("listener exploded")
		})
		Wrap(func(in *Context) error {
			inner = in
			rc = in.RenderContext()
			func() {
				defer func() {
					if recover() != nil && !rc.Written() {
						rc.WriteHeader(http.StatusInternalServerError)
					}
				}()
				rc.WriteHeader(http.StatusNotFound)
			}()
			return nil
		})(&silentWrapper{c.Response}, c.Request)
	})
	r.Get("/x", func(*Context) error { return errors.New("failed") })
	rec := httptest.NewRecorder()
	if p := hostile.Within(t, hostile.Deadline, func() {
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	}); p != nil {
		t.Fatalf("panicked: %v", p)
	}
	if !reflect.DeepEqual(innerSaid, []bool{true}) {
		t.Fatalf("the inner writer reported committed = %v; the excluded behaviour is [true] (a fix makes it [false]: flip this test)", innerSaid)
	}
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("status = %d, body %q; the excluded behaviour is an empty 200 (a fix makes it 500: flip this test)", rec.Code, rec.Body.String())
	}
	if !reflect.DeepEqual(tail, []int{200}) {
		t.Fatalf("the pending listener saw %v; the excluded behaviour is [200] (a fix makes it [500]: flip this test)", tail)
	}
}
