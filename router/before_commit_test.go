package router

import (
	"bufio"
	"context"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
)

// challengeOn401 is the middleware the hook exists for: it adds a header
// to whatever answers the request with a 401, whoever writes it.
func challengeOn401(next HandlerFunc) HandlerFunc {
	return func(c *Context) error {
		c.BeforeCommit(func(status int, w http.ResponseWriter) {
			if status == http.StatusUnauthorized {
				w.Header().Set("WWW-Authenticate", `Bearer realm="test"`)
			}
		})
		return next(c)
	}
}

// A middleware sees the status the response goes out with, whichever path
// answers: the handler itself, the default error path, or an installed
// error handler mapping a plain error after every middleware returned.
func TestBeforeCommit_ListenerSeesTheStatusEveryPathAnswersWith(t *testing.T) {
	plain := errors.New("no credentials")
	cases := []struct {
		name       string
		handler    HandlerFunc
		errHandler func(c *Context, err error, info ErrorInfo)
		wantStatus int
		wantHeader bool
	}{
		{
			name:    "installed error handler maps a plain error to 401",
			handler: func(c *Context) error { return plain },
			errHandler: func(c *Context, err error, info ErrorInfo) {
				c.Response.WriteHeader(http.StatusUnauthorized)
			},
			wantStatus: http.StatusUnauthorized,
			wantHeader: true,
		},
		{
			name: "handler writes 401 itself",
			handler: func(c *Context) error {
				c.Response.WriteHeader(http.StatusUnauthorized)
				return nil
			},
			wantStatus: http.StatusUnauthorized,
			wantHeader: true,
		},
		{
			name:       "default path answers an error naming 401",
			handler:    func(c *Context) error { return contract.NewHTTPError(http.StatusUnauthorized) },
			wantStatus: http.StatusUnauthorized,
			wantHeader: true,
		},
		{
			name: "200 carries no challenge",
			handler: func(c *Context) error {
				_, err := c.Response.Write([]byte("ok"))
				return err
			},
			wantStatus: http.StatusOK,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := New()
			if tc.errHandler != nil {
				r.SetErrorHandler(tc.errHandler)
			}
			r.Use(challengeOn401)
			r.Get("/x", tc.handler)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			got := rec.Result().Header.Get("WWW-Authenticate") != ""
			if got != tc.wantHeader {
				t.Fatalf("challenge header present = %v, want %v", got, tc.wantHeader)
			}
		})
	}
}

// Listeners run once each, the last registered first, with one status; a
// later registration does not displace an earlier one, past the inline
// slot too.
func TestBeforeCommit_ListenersRunOnceInOrderWithOneStatus(t *testing.T) {
	for _, n := range []int{1, 2, 5} {
		var order []int
		var statuses []int
		r := New()
		for i := 0; i < n; i++ {
			r.Use(func(next HandlerFunc) HandlerFunc {
				return func(c *Context) error {
					if !c.BeforeCommit(func(status int, w http.ResponseWriter) {
						order = append(order, i)
						statuses = append(statuses, status)
					}) {
						t.Errorf("registration %d refused", i)
					}
					return next(c)
				}
			})
		}
		r.Get("/x", func(c *Context) error {
			c.Response.WriteHeader(http.StatusTeapot)
			_, _ = c.Response.Write([]byte("body"))
			c.Response.WriteHeader(http.StatusOK)
			return nil
		})
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
		if len(order) != n {
			t.Fatalf("n=%d: %d listener runs, want %d", n, len(order), n)
		}
		for i := range order {
			if order[i] != n-1-i || statuses[i] != http.StatusTeapot {
				t.Fatalf("n=%d: run %d was listener %d with status %d", n, i, order[i], statuses[i])
			}
		}
	}
}

// A panicking listener is contained and spent: the boundary answers 500
// and reports the panic once, the listener that ran ahead of it is not
// run again, and the listener not yet attempted runs once, with the 500.
func TestBeforeCommit_PanickingListenerIsContained(t *testing.T) {
	var reports, recovered atomic.Int32
	var ahead, behind []int
	panics := 0
	r := New()
	r.SetErrorHandler(func(c *Context, err error, info ErrorInfo) {
		reports.Add(1)
		if info.Recovered {
			recovered.Add(1)
		}
		c.Response.WriteHeader(http.StatusInternalServerError)
	})
	r.Use(func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			c.BeforeCommit(func(status int, _ http.ResponseWriter) { behind = append(behind, status) })
			c.BeforeCommit(func(int, http.ResponseWriter) { panics++; panic("listener exploded") })
			c.BeforeCommit(func(status int, _ http.ResponseWriter) { ahead = append(ahead, status) })
			return next(c)
		}
	})
	r.Get("/x", func(c *Context) error {
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if reports.Load() != 1 || recovered.Load() != 1 {
		t.Fatalf("reports = %d (recovered %d), want one recovered report", reports.Load(), recovered.Load())
	}
	if panics != 1 {
		t.Fatalf("the panicking listener was attempted %d times, want 1", panics)
	}
	if !reflect.DeepEqual(ahead, []int{200}) {
		t.Fatalf("the listener ahead of the panic saw %v, want [200] once", ahead)
	}
	if !reflect.DeepEqual(behind, []int{500}) {
		t.Fatalf("the listener behind the panic saw %v, want [500] once", behind)
	}
}

// A listener that blocks holds only its own request: another request
// through the same router completes while it is blocked.
func TestBeforeCommit_BlockingListenerHoldsOnlyItsRequest(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	r := New()
	r.Use(func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			if c.Request.URL.Path == "/slow" {
				c.BeforeCommit(func(int, http.ResponseWriter) {
					close(entered)
					<-release
				})
			}
			return next(c)
		}
	})
	h := func(c *Context) error {
		c.Response.WriteHeader(http.StatusNoContent)
		return nil
	}
	r.Get("/slow", h)
	r.Get("/fast", h)

	slowDone := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/slow", nil))
		slowDone <- rec.Code
	}()
	<-entered
	fast := httptest.NewRecorder()
	r.ServeHTTP(fast, httptest.NewRequest(http.MethodGet, "/fast", nil))
	if fast.Code != http.StatusNoContent {
		t.Fatalf("the other request answered %d while a listener blocked", fast.Code)
	}
	select {
	case code := <-slowDone:
		t.Fatalf("the blocked request finished (%d) before its listener returned", code)
	default:
	}
	close(release)
	if code := <-slowDone; code != http.StatusNoContent {
		t.Fatalf("the blocked request answered %d once released", code)
	}
}

// hijackableRecorder is a recorder whose Hijack succeeds or fails as told.
type hijackableRecorder struct {
	*httptest.ResponseRecorder
	err      error
	hijacked int
}

func (h *hijackableRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.hijacked++
	if h.err != nil {
		return nil, nil, h.err
	}
	c1, c2 := net.Pipe()
	_ = c2.Close()
	return c1, bufio.NewReadWriter(bufio.NewReader(c1), bufio.NewWriter(c1)), nil
}

// A listener that calls back into the writer cannot commit, replace the
// status, take the connection or register: the response goes out once,
// with the status that was selected, after every listener returned.
func TestBeforeCommit_ReentrantListenerCannotCommit(t *testing.T) {
	var writeErr, hijackErr error
	var registered = true
	var runs int
	var c0 *Context
	r := New()
	r.Use(func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			c0 = c
			c.BeforeCommit(func(status int, w http.ResponseWriter) {
				runs++
				w.WriteHeader(http.StatusTeapot)
				w.(http.Flusher).Flush()
				_, writeErr = w.Write([]byte("from listener"))
				_, _, hijackErr = w.(http.Hijacker).Hijack()
				registered = c0.BeforeCommit(func(int, http.ResponseWriter) { runs += 100 })
				w.Header().Set("X-Listener", "ran")
			})
			return next(c)
		}
	})
	r.Get("/x", func(c *Context) error {
		c.Response.WriteHeader(http.StatusAccepted)
		_, err := c.Response.Write([]byte("body"))
		return err
	})
	rec := &hijackableRecorder{ResponseRecorder: httptest.NewRecorder()}
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if runs != 1 {
		t.Fatalf("listener runs = %d, want 1", runs)
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want the selected 202", rec.Code)
	}
	if rec.Body.String() != "body" {
		t.Fatalf("body = %q, want only the handler's", rec.Body.String())
	}
	if rec.Flushed {
		t.Fatal("a Flush from inside a listener reached the connection")
	}
	if writeErr == nil {
		t.Fatal("Write from inside a listener returned no error")
	}
	if hijackErr == nil || rec.hijacked != 0 {
		t.Fatalf("Hijack from inside a listener: err=%v, underlying calls=%d", hijackErr, rec.hijacked)
	}
	if registered {
		t.Fatal("BeforeCommit from inside a listener returned true")
	}
	if rec.Result().Header.Get("X-Listener") != "ran" {
		t.Fatal("the listener's header did not reach the response")
	}
}

func TestBeforeCommit_NilAndLateRegistrationAreRefused(t *testing.T) {
	var late, nilOK = true, true
	r := New()
	r.Get("/x", func(c *Context) error {
		nilOK = c.BeforeCommit(nil)
		c.Response.WriteHeader(http.StatusOK)
		late = c.BeforeCommit(func(int, http.ResponseWriter) { t.Error("a listener registered after the commit ran") })
		return nil
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	if nilOK {
		t.Fatal("BeforeCommit(nil) returned true")
	}
	if late {
		t.Fatal("BeforeCommit after the commit returned true")
	}
}

// How the response commits decides the status a listener gets: an empty
// response and a stream opened by Flush are 200, an informational status
// does not fire, 101 does.
func TestBeforeCommit_StatusSelection(t *testing.T) {
	cases := []struct {
		name    string
		handler HandlerFunc
		want    []int
	}{
		{"empty response finalized by the router", func(c *Context) error { return nil }, []int{200}},
		{"stream opened by Flush", func(c *Context) error {
			c.Response.(http.Flusher).Flush()
			_, _ = c.Response.Write([]byte("tick"))
			c.Response.(http.Flusher).Flush()
			return nil
		}, []int{200}},
		{"implicit Write", func(c *Context) error {
			_, err := c.Response.Write([]byte("x"))
			return err
		}, []int{200}},
		{"informational then final", func(c *Context) error {
			c.Response.WriteHeader(http.StatusEarlyHints)
			c.Response.WriteHeader(http.StatusCreated)
			return nil
		}, []int{201}},
		{"switching protocols", func(c *Context) error {
			c.Response.WriteHeader(http.StatusSwitchingProtocols)
			return nil
		}, []int{101}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []int
			r := New()
			r.Use(func(next HandlerFunc) HandlerFunc {
				return func(c *Context) error {
					c.BeforeCommit(func(status int, w http.ResponseWriter) { got = append(got, status) })
					return next(c)
				}
			})
			r.Get("/x", tc.handler)
			r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("listener statuses = %v, want %v", got, tc.want)
			}
		})
	}
}

// A raw Hijack has no HTTP status: a successful one fires nothing and
// drops the listeners; a failed or unsupported one leaves them armed for
// the response that follows.
func TestBeforeCommit_Hijack(t *testing.T) {
	serve := func(w http.ResponseWriter, after HandlerFunc) (statuses []int) {
		r := New()
		r.Use(func(next HandlerFunc) HandlerFunc {
			return func(c *Context) error {
				c.BeforeCommit(func(status int, w http.ResponseWriter) { statuses = append(statuses, status) })
				return next(c)
			}
		})
		r.Get("/x", func(c *Context) error {
			conn, _, err := c.Response.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return after(c)
		})
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		return statuses
	}
	nothing := func(c *Context) error { return nil }
	conflict := func(c *Context) error {
		c.Response.WriteHeader(http.StatusConflict)
		return nil
	}

	if got := serve(&hijackableRecorder{ResponseRecorder: httptest.NewRecorder()}, nothing); len(got) != 0 {
		t.Fatalf("a successful hijack fired listeners with %v", got)
	}
	failed := &hijackableRecorder{ResponseRecorder: httptest.NewRecorder(), err: errors.New("refused")}
	if got := serve(failed, conflict); !reflect.DeepEqual(got, []int{409}) {
		t.Fatalf("after a failed hijack listeners got %v, want [409]", got)
	}
	if got := serve(httptest.NewRecorder(), conflict); !reflect.DeepEqual(got, []int{409}) {
		t.Fatalf("after an unsupported hijack listeners got %v, want [409]", got)
	}
	if got := serve(httptest.NewRecorder(), nothing); !reflect.DeepEqual(got, []int{200}) {
		t.Fatalf("after an unsupported hijack and no response listeners got %v, want [200]", got)
	}
}

// Timeout runs the handler on a clone with no commit owner: registration
// inside it is refused, registration outside it sees the status Timeout's
// buffered response or its 503 goes out with.
func TestBeforeCommit_Timeout(t *testing.T) {
	t.Run("inside is refused", func(t *testing.T) {
		inside := true
		r := New()
		r.Use(Timeout(time.Minute))
		r.Get("/x", func(c *Context) error {
			inside = c.BeforeCommit(func(int, http.ResponseWriter) { t.Error("a listener registered under Timeout ran") })
			c.Response.WriteHeader(http.StatusOK)
			return nil
		})
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
		if inside {
			t.Fatal("BeforeCommit under Timeout returned true")
		}
	})
	t.Run("outside sees the flushed status", func(t *testing.T) {
		var got []int
		r := New()
		r.Use(func(next HandlerFunc) HandlerFunc {
			return func(c *Context) error {
				c.BeforeCommit(func(status int, w http.ResponseWriter) { got = append(got, status) })
				return next(c)
			}
		})
		r.Use(Timeout(time.Minute))
		r.Get("/x", func(c *Context) error {
			c.Response.WriteHeader(http.StatusAccepted)
			return nil
		})
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
		if !reflect.DeepEqual(got, []int{202}) {
			t.Fatalf("listener statuses = %v, want [202]", got)
		}
	})
	t.Run("outside sees the 503 of a timed out handler", func(t *testing.T) {
		var got []int
		release := make(chan struct{})
		defer close(release)
		r := New()
		r.Use(func(next HandlerFunc) HandlerFunc {
			return func(c *Context) error {
				c.BeforeCommit(func(status int, w http.ResponseWriter) { got = append(got, status) })
				return next(c)
			}
		})
		r.Use(Timeout(time.Millisecond))
		r.Get("/x", func(c *Context) error {
			<-release
			return nil
		})
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
		if !reflect.DeepEqual(got, []int{503}) {
			t.Fatalf("listener statuses = %v, want [503]", got)
		}
	})
}

// Concurrent requests through the pooled writer and context each run their
// own listeners once, with their own status.
func TestBeforeCommit_Concurrent(t *testing.T) {
	r := New()
	r.Use(func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			want := c.Request.URL.Path
			for i := 0; i < 3; i++ {
				c.BeforeCommit(func(status int, w http.ResponseWriter) {
					w.Header().Add("X-Seen", want)
					if (status == http.StatusNotFound) != (want == "/missing") {
						w.Header().Set("X-Wrong", "status")
					}
				})
			}
			return next(c)
		}
	})
	r.Get("/a", func(c *Context) error { return c.NoContent() })
	r.Get("/b", func(c *Context) error {
		_, err := c.Response.Write([]byte("b"))
		return err
	})
	r.Get("/empty", func(c *Context) error { return nil })
	paths := []string{"/a", "/b", "/empty", "/missing"}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				p := paths[(g+i)%len(paths)]
				rec := httptest.NewRecorder()
				r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
				seen := rec.Result().Header.Values("X-Seen")
				if len(seen) != 3 || seen[0] != p || seen[1] != p || seen[2] != p {
					t.Errorf("%s: listeners left %v", p, seen)
					return
				}
				if rec.Result().Header.Get("X-Wrong") != "" {
					t.Errorf("%s: a listener got another request's status", p)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// A released writer holds no listener, in the inline slot or behind the
// overflow slice's length, and a reset context holds no writer.
func TestBeforeCommit_PoolReleaseDropsListeners(t *testing.T) {
	rw := acquireResponseWriter(httptest.NewRecorder())
	c := &Context{Response: rw, commit: rw}
	for i := 0; i < 4; i++ {
		if !c.BeforeCommit(func(int, http.ResponseWriter) {}) {
			t.Fatalf("registration %d refused", i)
		}
	}
	releaseResponseWriter(rw)
	c.reset()
	if c.commit != nil {
		t.Fatal("a reset context still points at its writer")
	}
	if rw.listener != nil {
		t.Fatal("a released writer still holds its inline listener")
	}
	more := rw.more[:cap(rw.more)]
	for i, fn := range more {
		if fn != nil {
			t.Fatalf("a released writer still holds overflow listener %d", i)
		}
	}
	if len(rw.more) != 0 {
		t.Fatalf("a released writer's overflow length = %d", len(rw.more))
	}
}

// A listener panicking with http.ErrAbortHandler keeps that value's
// net/http meaning: it reaches the caller of ServeHTTP, nothing is
// written and nothing is reported.
func TestBeforeCommit_AbortPanicReachesNetHTTP(t *testing.T) {
	var reports atomic.Int32
	r := New()
	r.SetErrorHandler(func(*Context, error, ErrorInfo) { reports.Add(1) })
	r.Use(func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			c.BeforeCommit(func(int, http.ResponseWriter) { panic(http.ErrAbortHandler) })
			return next(c)
		}
	})
	r.Get("/x", func(c *Context) error {
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})
	w := &writeSpy{ResponseRecorder: httptest.NewRecorder()}
	escaped := func() (p any) {
		defer func() { p = recover() }()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		return nil
	}()
	if err, ok := escaped.(error); !ok || !errors.Is(err, http.ErrAbortHandler) {
		t.Fatalf("ServeHTTP ended with %v, want the http.ErrAbortHandler panic", escaped)
	}
	if w.wrote || reports.Load() != 0 {
		t.Fatalf("wrote = %v, reports = %d; want nothing written or reported", w.wrote, reports.Load())
	}
}

// An empty response runs its listeners with 200 and the router writes
// nothing: the underlying writer sees no WriteHeader, and RequestHandled
// records the 200 net/http will send.
func TestBeforeCommit_EmptyResponseIsNotWrittenByTheRouter(t *testing.T) {
	var got []int
	var handled []int
	r := New()
	r.SetEventDispatcher(func(_ context.Context, event interface{}) error {
		if ev, ok := event.(*RequestHandled); ok {
			handled = append(handled, ev.StatusCode)
		}
		return nil
	})
	var held *responseWriter
	r.Use(func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			held = c.Response.(*responseWriter)
			c.BeforeCommit(func(status int, w http.ResponseWriter) {
				got = append(got, status)
				w.Header().Set("X-Listener", "ran")
			})
			err := next(c)
			if held.Committed() {
				t.Error("an empty response is committed before the router is done with it")
			}
			return err
		}
	})
	r.Get("/x", func(*Context) error { return nil })
	w := &writeSpy{ResponseRecorder: httptest.NewRecorder()}
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if !reflect.DeepEqual(got, []int{200}) {
		t.Fatalf("listener statuses = %v, want [200]", got)
	}
	if w.wrote {
		t.Fatal("the router wrote a status for an empty response")
	}
	if w.Result().Header.Get("X-Listener") != "ran" {
		t.Fatal("the listener's header is not on the response")
	}
	if !reflect.DeepEqual(handled, []int{200}) {
		t.Fatalf("RequestHandled statuses = %v, want [200]", handled)
	}
}

// Wrap's Context has a commit owner: listeners see the status the handler
// or the default error response commits, and run with 200 for a response
// nothing wrote.
func TestBeforeCommit_Wrap(t *testing.T) {
	cases := []struct {
		name    string
		handler HandlerFunc
		want    int
	}{
		{"handler writes", func(c *Context) error { c.Response.WriteHeader(http.StatusAccepted); return nil }, 202},
		{"error answered", func(c *Context) error { return contract.NewHTTPError(http.StatusUnauthorized) }, 401},
		{"nothing written", func(c *Context) error { return nil }, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []int
			h := Wrap(func(c *Context) error {
				if !c.BeforeCommit(func(status int, w http.ResponseWriter) {
					got = append(got, status)
					w.Header().Set("X-Listener", "ran")
				}) {
					t.Error("registration under Wrap refused")
				}
				return tc.handler(c)
			})
			rec := httptest.NewRecorder()
			h(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
			if !reflect.DeepEqual(got, []int{tc.want}) {
				t.Fatalf("listener statuses = %v, want [%d]", got, tc.want)
			}
			if rec.Result().Header.Get("X-Listener") != "ran" {
				t.Fatal("the listener's header is not on the response")
			}
		})
	}
}

// A Context built by NewContext or NewTestContext keeps the writer it was
// given until a listener registers; the first registration binds the commit
// owner over it, and later ones join it.
func TestBeforeCommit_BuiltContextBindsOnDemand(t *testing.T) {
	builders := map[string]func() (*Context, *httptest.ResponseRecorder){
		"NewTestContext": func() (*Context, *httptest.ResponseRecorder) { return NewTestContext(http.MethodGet, "/x") },
		"NewContext": func() (*Context, *httptest.ResponseRecorder) {
			rec := httptest.NewRecorder()
			return NewContext(rec, httptest.NewRequest(http.MethodGet, "/x", nil)), rec
		},
	}
	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			c, rec := build()
			if c.Response != http.ResponseWriter(rec) {
				t.Fatal("the built Context does not hold the writer it was given")
			}
			if c.BeforeCommit(nil) || c.Response != http.ResponseWriter(rec) {
				t.Fatal("a refused registration changed the Context's writer")
			}
			var got []string
			for _, tag := range []string{"a", "b"} {
				if !c.BeforeCommit(func(status int, w http.ResponseWriter) {
					got = append(got, tag+http.StatusText(status))
					w.Header().Add("X-Listener", tag)
				}) {
					t.Fatalf("registration %s refused", tag)
				}
			}
			if http.NewResponseController(c.Response).Flush() != nil {
				t.Fatal("the bound writer does not reach the recorder")
			}
			_ = c.String(http.StatusOK, "body")
			if !reflect.DeepEqual(got, []string{"bOK", "aOK"}) {
				t.Fatalf("listeners ran as %v", got)
			}
			if !reflect.DeepEqual(rec.Result().Header.Values("X-Listener"), []string{"b", "a"}) {
				t.Fatalf("committed headers = %v", rec.Result().Header)
			}
		})
	}
	t.Run("already committed writer", func(t *testing.T) {
		rw := &responseWriter{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}
		rw.WriteHeader(http.StatusOK)
		c := NewContext(rw, httptest.NewRequest(http.MethodGet, "/x", nil))
		if c.BeforeCommit(func(int, http.ResponseWriter) {}) {
			t.Fatal("registration on a committed response returned true")
		}
	})
}

// The single-slot hook this mechanism replaced must not come back under
// its name: no Go file in the module mentions it.
func TestBeforeCommit_RemovedHookNameIsGone(t *testing.T) {
	removed := "Before" + "First" + "Write"
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == ".git" || n == "node_modules" || n == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), removed) {
			t.Errorf("%s mentions %s", path, removed)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// plainForwarder is an application writer that forwards Hijack and says
// nothing about commitment.
type plainForwarder struct{ http.ResponseWriter }

func (f *plainForwarder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return f.ResponseWriter.(http.Hijacker).Hijack()
}

// CanDeferBeforeCommit answers from the commit owner, whatever sits in
// c.Response: true while the response is uncommitted and the router or
// Wrap will run pending listeners, false otherwise, and it never binds an
// owner.
func TestCanDeferBeforeCommit(t *testing.T) {
	type probe struct{ start, pending, afterAnswer bool }
	viaRouter := func(answer func(c *Context), mws ...MiddlewareFunc) probe {
		var p probe
		r := New()
		r.Use(func(next HandlerFunc) HandlerFunc {
			return func(c *Context) error {
				c.Response = &plainForwarder{c.Response}
				return next(c)
			}
		})
		for _, mw := range mws {
			r.Use(mw)
		}
		r.Get("/x", func(c *Context) error {
			p.start = c.CanDeferBeforeCommit()
			c.BeforeCommit(func(int, http.ResponseWriter) {})
			p.pending = c.CanDeferBeforeCommit()
			answer(c)
			p.afterAnswer = c.CanDeferBeforeCommit()
			return nil
		})
		r.ServeHTTP(&hijackableRecorder{ResponseRecorder: httptest.NewRecorder()}, httptest.NewRequest(http.MethodGet, "/x", nil))
		return p
	}
	if got := viaRouter(func(*Context) {}); got != (probe{true, true, true}) {
		t.Errorf("router, nothing written: %+v, want all true", got)
	}
	if got := viaRouter(func(c *Context) { c.Response.WriteHeader(http.StatusOK) }); got != (probe{true, true, false}) {
		t.Errorf("router, committed: %+v, want false after the commit", got)
	}
	hijack := func(c *Context) {
		if conn, _, err := c.Response.(http.Hijacker).Hijack(); err == nil {
			_ = conn.Close()
		}
	}
	if got := viaRouter(hijack); got != (probe{true, true, false}) {
		t.Errorf("router, hijacked through a wrapper: %+v, want false after the takeover", got)
	}
	if got := viaRouter(func(*Context) {}, Timeout(time.Minute)); got != (probe{}) {
		t.Errorf("inside Timeout: %+v, want all false", got)
	}

	t.Run("Wrap", func(t *testing.T) {
		var before, after bool
		Wrap(func(c *Context) error {
			before = c.CanDeferBeforeCommit()
			c.Response.WriteHeader(http.StatusOK)
			after = c.CanDeferBeforeCommit()
			return nil
		})(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
		if !before || after {
			t.Errorf("Wrap: before = %v, after the commit = %v; want true, false", before, after)
		}
	})
	t.Run("built contexts", func(t *testing.T) {
		c, rec := NewTestContext(http.MethodGet, "/x")
		if c.CanDeferBeforeCommit() {
			t.Error("a built Context with no owner answered true")
		}
		if c.Response != http.ResponseWriter(rec) || c.commit != nil {
			t.Error("the question bound a commit owner")
		}
		c.BeforeCommit(func(int, http.ResponseWriter) {})
		if c.CanDeferBeforeCommit() {
			t.Error("a Context bound on demand answered true: nothing finalizes it")
		}
	})
	t.Run("borrowed owner keeps its finalizer", func(t *testing.T) {
		var borrowed bool
		r := New()
		r.Get("/x", func(c *Context) error {
			inner := NewContext(c.Response, c.Request)
			inner.BeforeCommit(func(int, http.ResponseWriter) {})
			borrowed = inner.CanDeferBeforeCommit()
			return nil
		})
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
		if !borrowed {
			t.Error("a Context built over the router's writer lost the router's finalizer")
		}
	})
}
