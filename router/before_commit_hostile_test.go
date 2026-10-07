package router

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/hostile"
)

// A status net/http rejects does not consume the listeners: the panic it
// raises is answered by the boundary's 500, and that is the status they
// run with, once.
func TestBeforeCommit_InvalidStatusLeavesListenersArmed(t *testing.T) {
	var got []int
	r := New()
	r.Use(func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			c.BeforeCommit(func(status int, w http.ResponseWriter) { got = append(got, status) })
			return next(c)
		}
	})
	r.Get("/x", func(c *Context) error {
		c.Response.WriteHeader(99)
		return nil
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !reflect.DeepEqual(got, []int{500}) {
		t.Fatalf("listener statuses = %v, want [500]", got)
	}
}

// A handler panics, and a listener panics while the boundary writes the
// 500 for it: the second panic is contained too. On a real server the
// client gets a complete 500 (with the body the fallback wrote, whatever
// Content-Length the listener left behind), both panics are reported as
// recovered, and the request is recorded once.
func TestBeforeCommit_ListenerPanicWhileAnsweringAPanic(t *testing.T) {
	for _, installed := range []bool{false, true} {
		name := "DefaultPath"
		if installed {
			name = "InstalledHandler"
		}
		t.Run(name, func(t *testing.T) {
			var (
				mu      sync.Mutex
				failed  []bool
				handled []int
				calls   int
			)
			r := NewV2()
			r.SetLogger(levelLogger{onError: func(string, ...any) {}})
			if installed {
				r.SetErrorHandler(func(c *Context, _ error, info ErrorInfo) {
					mu.Lock()
					calls++
					mu.Unlock()
					c.Response.WriteHeader(http.StatusInternalServerError)
					_, _ = c.Response.Write([]byte("handled"))
				})
			}
			r.SetEventDispatcher(func(_ context.Context, event interface{}) error {
				mu.Lock()
				defer mu.Unlock()
				switch ev := event.(type) {
				case *RequestFailed:
					failed = append(failed, ev.Recovered)
				case *RequestHandled:
					handled = append(handled, ev.StatusCode)
				}
				return nil
			})
			r.Use(func(next HandlerFunc) HandlerFunc {
				return func(c *Context) error {
					c.BeforeCommit(func(_ int, w http.ResponseWriter) {
						w.Header().Set("Content-Length", "100000")
						panic("listener exploded")
					})
					return next(c)
				}
			})
			r.Get("/boom", func(*Context) error { panic("a bug") })
			srv := httptest.NewServer(r)
			defer srv.Close()

			resp, err := srv.Client().Get(srv.URL + "/boom")
			if err != nil {
				t.Fatalf("GET: %v (the listener's panic escaped the router)", err)
			}
			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", resp.StatusCode)
			}
			if readErr != nil || len(body) == 0 {
				t.Fatalf("body = %q, read error %v; want the fallback's complete body", body, readErr)
			}
			srv.Close()
			mu.Lock()
			defer mu.Unlock()
			if !reflect.DeepEqual(failed, []bool{true, true}) {
				t.Errorf("RequestFailed recovered flags = %v, want one per panic", failed)
			}
			if !reflect.DeepEqual(handled, []int{500}) {
				t.Errorf("RequestHandled statuses = %v, want [500]", handled)
			}
			if installed && calls != 2 {
				t.Errorf("error handler calls = %d, want one per panic", calls)
			}
		})
	}
}

// A listener that set Content-Length and then panicked does not cut the
// contained 500 short: the client reads its whole body.
func TestBeforeCommit_PanickingListenerLeavesNoContentLength(t *testing.T) {
	r := NewV2()
	r.SetLogger(levelLogger{onError: func(string, ...any) {}})
	r.Use(func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			c.BeforeCommit(func(_ int, w http.ResponseWriter) {
				w.Header().Set("Content-Length", "2")
				panic("listener exploded")
			})
			return next(c)
		}
	})
	r.Get("/x", func(c *Context) error { return c.String(http.StatusOK, "ok") })
	srv := httptest.NewServer(r)
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL + "/x")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError || readErr != nil {
		t.Fatalf("status = %d, read error %v; want a complete 500", resp.StatusCode, readErr)
	}
	if len(body) <= 2 {
		t.Fatalf("body = %q, cut to the listener's Content-Length", body)
	}
}

// The 401 scenario on a real server: an installed error handler maps a
// plain error to 401 after every middleware returned, and the header a
// middleware's listener adds is on the wire.
func TestBeforeCommit_ChallengeReachesTheWire(t *testing.T) {
	r := New()
	r.SetErrorHandler(func(c *Context, _ error, _ ErrorInfo) {
		c.Response.WriteHeader(http.StatusUnauthorized)
	})
	r.Use(challengeOn401)
	r.Get("/x", func(*Context) error { return io.ErrUnexpectedEOF })
	srv := httptest.NewServer(r)
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL + "/x")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") == "" {
		t.Fatalf("response = %d %v, want a 401 carrying the challenge", resp.StatusCode, resp.Header)
	}
}

// Timeout commits its buffered response with no lock held: a listener that
// writes to the timeout writer the handler saw (it kept a reference) is
// not waiting on the flush that runs it.
func TestBeforeCommit_ListenerReentersTheTimeoutWriter(t *testing.T) {
	var buffered http.ResponseWriter
	ran := false
	r := New()
	r.Use(func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			c.BeforeCommit(func(int, http.ResponseWriter) {
				ran = true
				buffered.Header().Set("X-Late", "1")
				_, _ = buffered.Write([]byte("late"))
				buffered.WriteHeader(http.StatusTeapot)
			})
			return next(c)
		}
	})
	r.Use(Timeout(time.Minute))
	r.Get("/x", func(c *Context) error {
		buffered = c.Response
		return c.String(http.StatusAccepted, "body")
	})
	rec := httptest.NewRecorder()
	if p := hostile.Within(t, hostile.Deadline, func() {
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	}); p != nil {
		t.Fatalf("panicked: %v", p)
	}
	if !ran {
		t.Fatal("the listener did not run")
	}
	if rec.Code != http.StatusAccepted || rec.Body.String() != "body" {
		t.Fatalf("response = %d %q, want the handler's 202 and body", rec.Code, rec.Body.String())
	}
}

// A listener cannot be registered under Timeout, so the 503 of a timed out
// handler runs none the handler tried to register.
func TestBeforeCommit_TimeoutAnswerRunsNoHandlerListener(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	registered := make(chan bool, 1)
	r := New()
	r.Use(Timeout(time.Millisecond))
	r.Get("/x", func(c *Context) error {
		registered <- c.BeforeCommit(func(int, http.ResponseWriter) { t.Error("a listener registered under Timeout ran") })
		<-release
		return nil
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if <-registered {
		t.Fatal("BeforeCommit under Timeout returned true")
	}
}

// Every listener panics: each is attempted once, each panic is reported
// once, the retries end, and the client still gets a 500.
func TestBeforeCommit_EveryListenerPanics(t *testing.T) {
	const n = 5
	attempts := make([]int, n)
	reports := 0
	r := New()
	r.SetErrorHandler(func(c *Context, _ error, info ErrorInfo) {
		if info.Recovered {
			reports++
		}
		c.Response.WriteHeader(http.StatusInternalServerError)
	})
	r.Use(func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			for i := 0; i < n; i++ {
				c.BeforeCommit(func(int, http.ResponseWriter) { attempts[i]++; panic("listener exploded") })
			}
			return next(c)
		}
	})
	r.Get("/x", func(c *Context) error { return c.String(http.StatusOK, "ok") })
	rec := httptest.NewRecorder()
	if p := hostile.Within(t, hostile.Deadline, func() {
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	}); p != nil {
		t.Fatalf("panicked: %v", p)
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !reflect.DeepEqual(attempts, []int{1, 1, 1, 1, 1}) {
		t.Fatalf("attempts per listener = %v, want one each", attempts)
	}
	if reports != n {
		t.Fatalf("recovered reports = %d, want one per panic (%d)", reports, n)
	}
}

// The error handler panics while listeners are still pending behind a
// broken dispatch: that panic is not a listener's, so nothing is retried.
// It goes on to net/http and the pending listener is not run for it.
func TestBeforeCommit_ErrorHandlerPanicIsNotRetried(t *testing.T) {
	calls, tail := 0, 0
	r := New()
	r.SetErrorHandler(func(*Context, error, ErrorInfo) {
		calls++
		panic("error handler exploded")
	})
	r.Use(func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			c.BeforeCommit(func(int, http.ResponseWriter) { tail++ })
			c.BeforeCommit(func(int, http.ResponseWriter) { panic("listener exploded") })
			return next(c)
		}
	})
	r.Get("/x", func(c *Context) error { return c.String(http.StatusOK, "ok") })
	escaped := func() (p any) {
		defer func() { p = recover() }()
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
		return nil
	}()
	if escaped != "error handler exploded" {
		t.Fatalf("ServeHTTP ended with %v, want the error handler's panic", escaped)
	}
	if calls != 1 || tail != 0 {
		t.Fatalf("error handler calls = %d, pending listener runs = %d; want 1 and 0", calls, tail)
	}
}

// A listener left pending by a broken dispatch aborts when it runs with
// the boundary's answer: the abort reaches net/http, and nothing more is
// reported or retried.
func TestBeforeCommit_AbortFromAResumedListener(t *testing.T) {
	reports := 0
	r := New()
	r.SetErrorHandler(func(c *Context, _ error, _ ErrorInfo) {
		reports++
		c.Response.WriteHeader(http.StatusInternalServerError)
	})
	r.Use(func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			c.BeforeCommit(func(int, http.ResponseWriter) { panic(http.ErrAbortHandler) })
			c.BeforeCommit(func(int, http.ResponseWriter) { panic("listener exploded") })
			return next(c)
		}
	})
	r.Get("/x", func(c *Context) error { return c.String(http.StatusOK, "ok") })
	w := &writeSpy{ResponseRecorder: httptest.NewRecorder()}
	escaped := func() (p any) {
		defer func() { p = recover() }()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		return nil
	}()
	if err, ok := escaped.(error); !ok || err != http.ErrAbortHandler {
		t.Fatalf("ServeHTTP ended with %v, want http.ErrAbortHandler", escaped)
	}
	if reports != 1 || w.wrote {
		t.Fatalf("reports = %d, wrote = %v; want the first panic reported once and nothing written", reports, w.wrote)
	}
}

// The listeners a broken dispatch left pending get the status the error
// handler answers the panic with, whatever it is.
func TestBeforeCommit_ResumedListenersSeeTheHandlersStatus(t *testing.T) {
	var got []int
	r := New()
	r.SetErrorHandler(func(c *Context, _ error, _ ErrorInfo) {
		c.Response.WriteHeader(http.StatusServiceUnavailable)
	})
	r.Use(func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			c.BeforeCommit(func(status int, w http.ResponseWriter) {
				got = append(got, status)
				w.Header().Set("X-Listener", "ran")
			})
			c.BeforeCommit(func(int, http.ResponseWriter) { panic("listener exploded") })
			return next(c)
		}
	})
	r.Get("/x", func(c *Context) error { return c.String(http.StatusOK, "ok") })
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusServiceUnavailable || rec.Result().Header.Get("X-Listener") != "ran" {
		t.Fatalf("response = %d %v, want the 503 with the listener's header", rec.Code, rec.Result().Header)
	}
	if !reflect.DeepEqual(got, []int{503}) {
		t.Fatalf("the pending listener saw %v, want [503]", got)
	}
}

// discardWriter is a reusable sink: a response writer that keeps nothing.
type discardWriter struct{ h http.Header }

func (d *discardWriter) Header() http.Header         { return d.h }
func (d *discardWriter) Write(b []byte) (int, error) { return len(b), nil }
func (d *discardWriter) WriteHeader(int)             {}

// BenchmarkResponseWriterCommit isolates the writer: acquire, register,
// commit with a status and a body, release, over a reusable sink, with
// zero, one and two listeners.
func BenchmarkResponseWriterCommit(b *testing.B) {
	body := []byte("ok")
	for listeners := 0; listeners <= 2; listeners++ {
		b.Run([]string{"listeners=0", "listeners=1", "listeners=2"}[listeners], func(b *testing.B) {
			sink := &discardWriter{h: make(http.Header)}
			b.ReportAllocs()
			for b.Loop() {
				rw := acquireResponseWriter(sink)
				for i := 0; i < listeners; i++ {
					rw.addListener(benchNoopListener)
				}
				rw.WriteHeader(http.StatusOK)
				_, _ = rw.Write(body)
				releaseResponseWriter(rw)
			}
		})
	}
}

// An installed error handler recovers a listener's panic out of its own
// write and then panics itself: that panic is the handler's, not a
// listener's, so the boundary is not entered again and the listener still
// pending is not run for it.
func TestBeforeCommit_ErrorHandlerPanicAfterCatchingAListenerPanic(t *testing.T) {
	calls, pending := 0, 0
	r := New()
	r.SetErrorHandler(func(c *Context, _ error, _ ErrorInfo) {
		calls++
		func() {
			defer func() { _ = recover() }()
			c.Response.WriteHeader(http.StatusInternalServerError)
		}()
		panic("error handler exploded")
	})
	r.Use(func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			c.BeforeCommit(func(int, http.ResponseWriter) { pending++ })
			c.BeforeCommit(func(int, http.ResponseWriter) { panic("listener exploded") })
			return next(c)
		}
	})
	r.Get("/x", func(*Context) error { panic("a bug") })
	escaped := func() (p any) {
		defer func() { p = recover() }()
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
		return nil
	}()
	if escaped != "error handler exploded" {
		t.Fatalf("ServeHTTP ended with %v, want the error handler's panic", escaped)
	}
	if calls != 1 || pending != 0 {
		t.Fatalf("error handler calls = %d, pending listener runs = %d; want 1 and 0", calls, pending)
	}
}
