package router

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// The handler panics before writing anything. The error handler registers
// an ordinary listener and then a panicking one, and answers 500: the
// panicking listener is contained, the answer is attempted again and the
// ordinary listener runs once, with the 500 the client gets.
func TestBeforeCommit_ListenersRegisteredByTheErrorHandler(t *testing.T) {
	var seen []int
	calls := 0
	r := New()
	r.SetErrorHandler(func(c *Context, _ error, _ ErrorInfo) {
		calls++
		c.BeforeCommit(func(status int, _ http.ResponseWriter) { seen = append(seen, status) })
		c.BeforeCommit(func(int, http.ResponseWriter) { panic("listener exploded") })
		c.Response.WriteHeader(http.StatusInternalServerError)
	})
	r.Get("/x", func(*Context) error { panic("a bug") })
	rec := httptest.NewRecorder()
	var escaped any
	if p := hostile.Within(t, hostile.Deadline, func() {
		defer func() { escaped = recover() }()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	}); p != nil {
		t.Fatalf("panicked: %v", p)
	}
	if escaped != nil {
		t.Fatalf("the listener's panic escaped the router: %v", escaped)
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !reflect.DeepEqual(seen, []int{500}) {
		t.Fatalf("the ordinary listener saw %v, want [500] once", seen)
	}
	if calls != 2 {
		t.Fatalf("error handler calls = %d, want 2 (the bug, then the listener's panic)", calls)
	}
}

// The destination's Header would panic the next time it is called, and a
// listener panics: closing the broken dispatch and resuming it make no
// call into the destination, so the listener's panic reaches the router,
// the dispatch state is restored, the boundary's 500 goes out and the
// remaining listener runs once, with it.
func TestBeforeCommit_HeaderCleanupPanicDoesNotReplaceTheListenerPanic(t *testing.T) {
	var seen []int
	r := New()
	r.SetErrorHandler(func(c *Context, _ error, _ ErrorInfo) {
		c.Response.WriteHeader(http.StatusInternalServerError)
	})
	var w *headerPanicsOnce
	r.Use(func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			c.BeforeCommit(func(status int, _ http.ResponseWriter) { seen = append(seen, status) })
			c.BeforeCommit(func(int, http.ResponseWriter) {
				// The next Header call on the destination panics; the
				// writer must not need one to close the dispatch.
				w.panicked = false
				panic("listener exploded")
			})
			return next(c)
		}
	})
	r.Get("/x", func(c *Context) error {
		c.Response.WriteHeader(http.StatusAccepted)
		return nil
	})
	w = &headerPanicsOnce{ResponseRecorder: httptest.NewRecorder(), panicked: true}
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if !reflect.DeepEqual(seen, []int{500}) {
		t.Fatalf("the remaining listener saw %v, want [500] once", seen)
	}
}

// TestBeforeCommit_PanicTable runs the rows of the panic table: for every
// place a panic can arise between a commit being attempted and the
// response being committed, what the client gets, what the listeners not
// yet attempted see, and what leaves the router. Rows 13 and 14 (the error
// pipeline's render and last-resort write) are in problem/routerbridge.
func TestBeforeCommit_PanicTable(t *testing.T) {
	type row struct {
		name string
		// build wires the router; tail records what the pending listener
		// saw. It may return the writer to serve through.
		build   func(r *VelocityRouterV2, tail *[]int) http.ResponseWriter
		code    int
		tail    []int
		escapes string // "", "abort", "value"
		// check, when set, looks at the committed response.
		check func(t *testing.T, rec *httptest.ResponseRecorder)
	}
	pending := func(tail *[]int) MiddlewareFunc {
		return func(next HandlerFunc) HandlerFunc {
			return func(c *Context) error {
				c.BeforeCommit(func(status int, _ http.ResponseWriter) { *tail = append(*tail, status) })
				return next(c)
			}
		}
	}
	listener := func(fn func(c *Context)) MiddlewareFunc {
		return func(next HandlerFunc) HandlerFunc {
			return func(c *Context) error {
				c.BeforeCommit(func(int, http.ResponseWriter) { fn(c) })
				return next(c)
			}
		}
	}
	explode := func(*Context) { panic("listener exploded") }
	write := func(status int) HandlerFunc {
		return func(c *Context) error {
			c.Response.WriteHeader(status)
			return nil
		}
	}
	answer500 := func(c *Context, _ error, _ ErrorInfo) { c.Response.WriteHeader(http.StatusInternalServerError) }
	rows := []row{
		{name: "1 listener, write in the handler chain", code: 500, tail: []int{500},
			build: func(r *VelocityRouterV2, tail *[]int) http.ResponseWriter {
				r.SetErrorHandler(answer500)
				r.Use(pending(tail))
				r.Use(listener(explode))
				r.Get("/x", write(http.StatusAccepted))
				return nil
			}},
		{name: "2 listener, finalize of an empty response", code: 500, tail: []int{500},
			build: func(r *VelocityRouterV2, tail *[]int) http.ResponseWriter {
				r.SetErrorHandler(answer500)
				r.Use(pending(tail))
				r.Use(listener(explode))
				r.Get("/x", func(*Context) error { return nil })
				return nil
			}},
		{name: "3 listener, during the boundary's answer", code: 500, tail: []int{500},
			build: func(r *VelocityRouterV2, tail *[]int) http.ResponseWriter {
				r.SetErrorHandler(answer500)
				r.Use(pending(tail))
				r.Use(listener(explode))
				r.Get("/x", func(*Context) error { panic("a bug") })
				return nil
			}},
		{name: "4 listener the error handler registered", code: 500, tail: []int{500},
			build: func(r *VelocityRouterV2, tail *[]int) http.ResponseWriter {
				r.SetErrorHandler(func(c *Context, _ error, _ ErrorInfo) {
					c.BeforeCommit(func(status int, _ http.ResponseWriter) { *tail = append(*tail, status) })
					c.BeforeCommit(func(int, http.ResponseWriter) { panic("listener exploded") })
					c.Response.WriteHeader(http.StatusInternalServerError)
				})
				r.Get("/x", func(*Context) error { panic("a bug") })
				return nil
			}},
		{name: "5 listener of another owner", code: 200, tail: nil, escapes: "value",
			build: func(r *VelocityRouterV2, tail *[]int) http.ResponseWriter {
				r.SetErrorHandler(func(c *Context, _ error, _ ErrorInfo) {
					Wrap(func(inner *Context) error {
						inner.BeforeCommit(func(int, http.ResponseWriter) { panic("inner listener exploded") })
						inner.Response.WriteHeader(http.StatusInternalServerError)
						return nil
					})(c.Response, c.Request)
				})
				r.Use(pending(tail))
				r.Get("/x", func(*Context) error { panic("a bug") })
				return nil
			}},
		{name: "6 abort from a listener", code: 200, tail: nil, escapes: "abort",
			build: func(r *VelocityRouterV2, tail *[]int) http.ResponseWriter {
				r.SetErrorHandler(answer500)
				r.Use(pending(tail))
				r.Use(listener(func(*Context) { panic(http.ErrAbortHandler) }))
				r.Get("/x", write(http.StatusAccepted))
				return nil
			}},
		{name: "7 error handler's own panic after recovering a listener's", code: 200, tail: nil, escapes: "value",
			build: func(r *VelocityRouterV2, tail *[]int) http.ResponseWriter {
				r.SetErrorHandler(func(c *Context, _ error, _ ErrorInfo) {
					func() {
						defer func() { _ = recover() }()
						c.Response.WriteHeader(http.StatusInternalServerError)
					}()
					panic("error handler exploded")
				})
				r.Use(pending(tail))
				r.Use(listener(explode))
				r.Get("/x", func(*Context) error { panic("a bug") })
				return nil
			}},
		{name: "9 closing a broken dispatch makes no call into a destination whose Header would panic", code: 500, tail: []int{500},
			build: func(r *VelocityRouterV2, tail *[]int) http.ResponseWriter {
				w := &headerPanicsOnce{ResponseRecorder: httptest.NewRecorder(), panicked: true}
				r.SetErrorHandler(answer500)
				r.Use(pending(tail))
				r.Use(listener(func(*Context) { w.panicked = false; panic("listener exploded") }))
				r.Get("/x", write(http.StatusAccepted))
				return w
			}},
		{name: "9 a length the listener set does not outlive a destination whose Header then panics", code: 500, tail: []int{500},
			build: func(r *VelocityRouterV2, tail *[]int) http.ResponseWriter {
				w := &headerPanicsOnce{ResponseRecorder: httptest.NewRecorder(), panicked: true}
				r.SetErrorHandler(func(c *Context, _ error, _ ErrorInfo) {
					c.Response.WriteHeader(http.StatusInternalServerError)
					_, _ = c.Response.Write([]byte("a body longer than two bytes"))
				})
				r.Use(pending(tail))
				r.Use(func(next HandlerFunc) HandlerFunc {
					return func(c *Context) error {
						c.BeforeCommit(func(_ int, lw http.ResponseWriter) {
							lw.Header().Set("Content-Length", "2")
							w.panicked = false
							panic("listener exploded")
						})
						return next(c)
					}
				})
				r.Get("/x", write(http.StatusAccepted))
				return w
			},
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				if got := rec.Result().Header.Get("Content-Length"); got != "" {
					t.Fatalf("the fallback went out under the listener's Content-Length %q", got)
				}
			}},
		{name: "10 an invalid status is refused by the owner, whatever the destination does with it", code: 500, tail: []int{500},
			build: func(r *VelocityRouterV2, tail *[]int) http.ResponseWriter {
				r.SetErrorHandler(answer500)
				r.Use(pending(tail))
				r.Get("/x", write(99))
				return &lenientWriter{ResponseRecorder: httptest.NewRecorder()}
			},
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				if rec.Result().Header.Get("X-Committed-Early") != "" {
					t.Fatal("the destination was handed the invalid status")
				}
			}},
		{name: "10 destination refuses an invalid status", code: 500, tail: []int{500},
			build: func(r *VelocityRouterV2, tail *[]int) http.ResponseWriter {
				r.SetErrorHandler(answer500)
				r.Use(pending(tail))
				r.Get("/x", write(99))
				return nil
			}},
		{name: "16 destination Header panics when the first dispatch fetches the header map", code: 500, tail: []int{500},
			build: func(r *VelocityRouterV2, tail *[]int) http.ResponseWriter {
				r.SetErrorHandler(answer500)
				r.Use(pending(tail))
				r.Get("/x", write(http.StatusAccepted))
				return &headerPanicsOnce{ResponseRecorder: httptest.NewRecorder()}
			}},
		{name: "18 listener writes through the Context's render adapter during dispatch, then panics", code: 500, tail: []int{500},
			build: func(r *VelocityRouterV2, tail *[]int) http.ResponseWriter {
				r.SetErrorHandler(func(c *Context, _ error, _ ErrorInfo) {
					rc := c.RenderContext()
					c.BeforeCommit(func(status int, _ http.ResponseWriter) { *tail = append(*tail, status) })
					c.BeforeCommit(func(int, http.ResponseWriter) {
						// Refused by the owner: a listener changes headers
						// only. The wrapper must not believe it wrote.
						rc.WriteHeader(http.StatusNoContent)
						panic("listener exploded")
					})
					// A render with its own recovery and last resort, as the
					// error pipeline has: it falls back only when nothing
					// was written.
					func() {
						defer func() {
							if recover() != nil && !rc.Written() {
								rc.WriteHeader(http.StatusInternalServerError)
							}
						}()
						rc.WriteHeader(http.StatusNotFound)
					}()
				})
				r.Get("/x", func(*Context) error { return errors.New("failed") })
				return nil
			}},
		{name: "18 listener writes through a render adapter over the writer during dispatch, then panics", code: 500, tail: []int{500},
			build: func(r *VelocityRouterV2, tail *[]int) http.ResponseWriter {
				r.SetErrorHandler(func(c *Context, _ error, _ ErrorInfo) {
					rc := contract.NewRenderContext(c.Response, c.Request)
					c.BeforeCommit(func(status int, _ http.ResponseWriter) { *tail = append(*tail, status) })
					c.BeforeCommit(func(int, http.ResponseWriter) {
						// Refused by the owner: a listener changes headers
						// only. The wrapper must not believe it wrote.
						rc.WriteHeader(http.StatusNoContent)
						panic("listener exploded")
					})
					// A render with its own recovery and last resort, as the
					// error pipeline has: it falls back only when nothing
					// was written.
					func() {
						defer func() {
							if recover() != nil && !rc.Written() {
								rc.WriteHeader(http.StatusInternalServerError)
							}
						}()
						rc.WriteHeader(http.StatusNotFound)
					}()
				})
				r.Get("/x", func(*Context) error { return errors.New("failed") })
				return nil
			}},
		{name: "11 destination panics on a valid status after a clean dispatch", code: 500, tail: []int{202},
			build: func(r *VelocityRouterV2, tail *[]int) http.ResponseWriter {
				r.SetErrorHandler(answer500)
				r.Use(pending(tail))
				r.Get("/x", write(http.StatusAccepted))
				return &panicOnceWriter{ResponseRecorder: httptest.NewRecorder()}
			}},
	}
	for _, tt := range rows {
		t.Run(tt.name, func(t *testing.T) {
			var tail []int
			r := New()
			w := tt.build(r, &tail)
			rec, _ := w.(*httptest.ResponseRecorder)
			switch v := w.(type) {
			case nil:
				rec = httptest.NewRecorder()
				w = rec
			case *headerPanicsOnce:
				rec = v.ResponseRecorder
			case *panicOnceWriter:
				rec = v.ResponseRecorder
			case *lenientWriter:
				rec = v.ResponseRecorder
			}
			var escaped any
			if p := hostile.Within(t, hostile.Deadline, func() {
				defer func() { escaped = recover() }()
				r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
			}); p != nil {
				t.Fatalf("panicked: %v", p)
			}
			got := ""
			if escaped != nil {
				got = "value"
				if isAbortPanic(escaped) {
					got = "abort"
				}
			}
			if got != tt.escapes {
				t.Fatalf("left the router: %q (%v), want %q", got, escaped, tt.escapes)
			}
			if rec.Code != tt.code {
				t.Fatalf("status = %d, want %d", rec.Code, tt.code)
			}
			if !reflect.DeepEqual(tail, tt.tail) {
				t.Fatalf("the pending listener saw %v, want %v", tail, tt.tail)
			}
			if tt.check != nil {
				tt.check(t, rec)
			}
		})
	}
}

// lenientWriter is a destination that does not refuse an invalid status:
// it answers 500 in its place and marks the response.
type lenientWriter struct {
	*httptest.ResponseRecorder
}

func (w *lenientWriter) WriteHeader(code int) {
	if code < 100 || code > 999 {
		w.Header().Set("X-Committed-Early", "1")
		code = http.StatusInternalServerError
	}
	w.ResponseRecorder.WriteHeader(code)
}

// A listener panics with a typed nil *panicerr.Listener while the boundary
// answers. It is that listener's value, not a mark: the writer marks it
// like any other, the answer is attempted again and the listener still
// pending runs once, with the 500 the client gets.
func TestBeforeCommit_ListenerPanicsWithATypedNilMark(t *testing.T) {
	var seen []int
	r := New()
	r.SetErrorHandler(func(c *Context, _ error, _ ErrorInfo) {
		c.BeforeCommit(func(status int, _ http.ResponseWriter) { seen = append(seen, status) })
		c.BeforeCommit(func(int, http.ResponseWriter) { panic((*panicerr.Listener)(nil)) })
		c.Response.WriteHeader(http.StatusInternalServerError)
	})
	// The handler panics, so the listener's value arrives while the
	// boundary is already answering a panic.
	r.Get("/x", func(*Context) error { panic("a bug") })
	rec := httptest.NewRecorder()
	var escaped any
	if p := hostile.Within(t, hostile.Deadline, func() {
		defer func() { escaped = recover() }()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	}); p != nil {
		t.Fatalf("panicked: %v", p)
	}
	if escaped != nil {
		t.Fatalf("the listener's panic escaped the router: %v", escaped)
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !reflect.DeepEqual(seen, []int{500}) {
		t.Fatalf("the pending listener saw %v, want [500] once", seen)
	}
}
