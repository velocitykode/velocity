package schemes

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gorilla "github.com/gorilla/websocket"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/websocket"
)

// sessionApp is a router with the session middleware and two routes: /put
// stores a value in the session and answers through answer, /read returns
// the stored value. Outer middleware is mounted ahead of the session's.
func sessionApp(t *testing.T, scheme *SessionScheme, answer router.HandlerFunc, outer ...router.MiddlewareFunc) *router.VelocityRouterV2 {
	t.Helper()
	r := router.New()
	for _, mw := range outer {
		r.Use(mw)
	}
	r.Use(scheme.SessionMiddleware())
	r.Get("/visit", func(c *router.Context) error { return c.NoContent() })
	r.Get("/put", func(c *router.Context) error {
		scheme.Session(c.Request).Put("k", "v")
		return answer(c)
	})
	r.Get("/read", func(c *router.Context) error {
		return c.String(http.StatusOK, fmt.Sprint(scheme.Session(c.Request).Get("k")))
	})
	return r
}

// visit makes the first request of a visitor and returns its session cookie.
func visit(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/visit", nil))
	for _, c := range rec.Result().Cookies() {
		if c.Name == "vel_session" {
			return c
		}
	}
	t.Fatal("the first response set no session cookie")
	return nil
}

// readStored returns what /read answers for the visitor holding cookie.
func readStored(t *testing.T, h http.Handler, cookie *http.Cookie) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/read", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Body.String()
}

// The session middleware registers its save beside other listeners: all
// run once, the last registered first, with one status, and the session
// cookie is in the response whichever path answers.
func TestSessionMiddleware_SavesBesideOtherListeners(t *testing.T) {
	answers := map[string]struct {
		handler router.HandlerFunc
		status  int
	}{
		"handler writes":   {func(c *router.Context) error { return c.String(http.StatusAccepted, "ok") }, http.StatusAccepted},
		"boundary answers": {func(c *router.Context) error { return fmt.Errorf("failed") }, http.StatusInternalServerError},
		"nothing answers":  {func(c *router.Context) error { return nil }, http.StatusOK},
	}
	for name, tt := range answers {
		t.Run(name, func(t *testing.T) {
			scheme, _ := storeScheme(t, false)
			var order []string
			listen := func(tag string) router.MiddlewareFunc {
				return func(next router.HandlerFunc) router.HandlerFunc {
					return func(c *router.Context) error {
						c.BeforeCommit(func(status int, w http.ResponseWriter) {
							order = append(order, fmt.Sprintf("%s:%d:%d", tag, status, len(w.Header().Values("Set-Cookie"))))
						})
						return next(c)
					}
				}
			}
			r := router.New()
			r.Use(listen("outer"))
			r.Use(scheme.SessionMiddleware())
			r.Use(listen("inner"))
			r.Get("/x", func(c *router.Context) error {
				scheme.Session(c.Request).Put("k", "v")
				return tt.handler(c)
			})
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}
			if n := len(sessionLines(rec)); n != 1 {
				t.Fatalf("response carries %d session cookies, want 1", n)
			}
			// The listener of the middleware inside the session's runs
			// ahead of the save and sees no cookie yet; the one of the
			// middleware outside it runs behind the save and sees it.
			want := []string{fmt.Sprintf("inner:%d:0", tt.status), fmt.Sprintf("outer:%d:1", tt.status)}
			if fmt.Sprint(order) != fmt.Sprint(want) {
				t.Fatalf("listeners ran as %v, want %v", order, want)
			}
		})
	}
}

// A route wrapped in router.Timeout still saves its session: the session
// middleware sits outside Timeout and its listener runs when the buffered
// response is committed. Mounted inside Timeout, where no listener can
// register, the save happens as the handler returns, into the buffer.
func TestSessionMiddleware_SavesAroundTimeout(t *testing.T) {
	for _, inside := range []bool{false, true} {
		for _, serverSide := range []bool{false, true} {
			t.Run(fmt.Sprintf("inside=%v/server=%v", inside, serverSide), func(t *testing.T) {
				scheme, _ := storeScheme(t, serverSide)
				r := router.New()
				if inside {
					r.Use(router.Timeout(time.Minute))
					r.Use(scheme.SessionMiddleware())
				} else {
					r.Use(scheme.SessionMiddleware())
					r.Use(router.Timeout(time.Minute))
				}
				r.Get("/put", func(c *router.Context) error {
					scheme.Session(c.Request).Put("k", "v")
					return c.String(http.StatusOK, "ok")
				})
				r.Get("/read", func(c *router.Context) error {
					return c.String(http.StatusOK, fmt.Sprint(scheme.Session(c.Request).Get("k")))
				})
				rec := httptest.NewRecorder()
				r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/put", nil))
				var cookie *http.Cookie
				for _, c := range rec.Result().Cookies() {
					if c.Name == "vel_session" {
						cookie = c
					}
				}
				if cookie == nil {
					t.Fatalf("no session cookie in the committed response: %v", rec.Result().Header)
				}
				if got := readStored(t, r, cookie); got != "v" {
					t.Fatalf("the next request read %q, want the saved value", got)
				}
			})
		}
	}
}

// A handler that takes the connection over runs no commit listener, so the
// session middleware saves when the handler returns: a server-side session
// keeps what the handler stored. A handler that holds the connection saves
// when it lets go, not before.
func TestSessionMiddleware_TakeoverSavesWhenTheHandlerReturns(t *testing.T) {
	scheme, _ := storeScheme(t, true)
	hijacked := make(chan struct{})
	release := make(chan struct{})
	r := sessionApp(t, scheme, func(c *router.Context) error {
		conn, _, err := c.Response.(http.Hijacker).Hijack()
		if err != nil {
			return err
		}
		close(hijacked)
		<-release
		return conn.Close()
	})
	cookie := visit(t, r)

	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequest(http.MethodGet, "/put", nil)
		req.AddCookie(cookie)
		r.ServeHTTP(&hijackingResponseWriter{ResponseWriter: httptest.NewRecorder()}, req)
	}()
	<-hijacked
	if got := readStored(t, r, cookie); got == "v" {
		t.Fatal("the session was saved while the handler still held the connection")
	}
	close(release)
	<-done
	if got := readStored(t, r, cookie); got != "v" {
		t.Fatalf("after the takeover the next request read %q, want the saved value", got)
	}
}

// The framework's WebSocket handler returns once the upgrade is done, so a
// session changed ahead of the upgrade is saved right after it.
func TestSessionMiddleware_WebSocketUpgradeSavesTheSession(t *testing.T) {
	scheme, _ := storeScheme(t, true)
	ws := websocket.New(websocket.DefaultConfig())
	if err := ws.Start(); err != nil {
		t.Fatalf("websocket start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Shutdown(context.Background()) })
	returned := make(chan struct{}, 1)
	// Mounted ahead of the session middleware, so it sees the request
	// leave it: the save has happened by then.
	left := func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			err := next(c)
			if c.Request.URL.Path == "/put" {
				returned <- struct{}{}
			}
			return err
		}
	}
	r := sessionApp(t, scheme, func(c *router.Context) error {
		ws.HandleConnection(c.Response, c.Request)
		return nil
	}, left)
	cookie := visit(t, r)

	srv := httptest.NewServer(r)
	defer srv.Close()
	header := http.Header{"Origin": {srv.URL}, "Cookie": {cookie.Name + "=" + cookie.Value}}
	conn, resp, err := gorilla.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/put", header)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade answered %d", resp.StatusCode)
	}
	<-returned
	if got := readStored(t, r, cookie); got != "v" {
		t.Fatalf("after the upgrade the next request read %q, want the saved value", got)
	}
}

// On a Context the router did not populate the first registration binds
// the commit owner: the session cookie is in the response a handler writes
// through router.Wrap, and in one nothing wrote on a built Context.
func TestSessionMiddleware_SavesOutsideARouter(t *testing.T) {
	scheme, _ := storeScheme(t, false)
	put := func(c *router.Context) error {
		scheme.Session(c.Request).Put("k", "v")
		return c.String(http.StatusOK, "ok")
	}
	empty := func(c *router.Context) error {
		scheme.Session(c.Request).Put("k", "v")
		return nil
	}
	for name, h := range map[string]router.HandlerFunc{"body": put, "empty": empty} {
		t.Run("Wrap/"+name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.Wrap(scheme.SessionMiddleware()(h))(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			if n := len(sessionLines(rec)); n != 1 {
				t.Fatalf("response carries %d session cookies, want 1", n)
			}
		})
		t.Run("NewTestContext/"+name, func(t *testing.T) {
			c, rec := router.NewTestContext(http.MethodGet, "/")
			if err := scheme.SessionMiddleware()(h)(c); err != nil {
				t.Fatal(err)
			}
			if n := len(sessionLines(rec)); n != 1 {
				t.Fatalf("response carries %d session cookies, want 1", n)
			}
		})
	}
}

// Listeners run the last registered first, so a listener of a middleware
// mounted inside the session's runs ahead of the session's save: a session
// change it makes is saved, and a deletion of the session cookie it makes
// ends the session. A listener of a middleware mounted outside runs behind
// the save: its session change is not saved and adds no second cookie.
func TestSessionMiddleware_ListenerChangesAroundTheSave(t *testing.T) {
	cases := []struct {
		name   string
		inside bool
		change func(scheme *SessionScheme, c *router.Context, w http.ResponseWriter)
		want   string
	}{
		{"inner change is saved", true, func(s *SessionScheme, c *router.Context, _ http.ResponseWriter) {
			s.Session(c.Request).Put("k", "listener")
		}, "listener"},
		{"inner cookie deletion ends the session", true, func(s *SessionScheme, _ *router.Context, w http.ResponseWriter) {
			http.SetCookie(w, &http.Cookie{Name: "vel_session", Value: "", Path: "/", MaxAge: -1})
		}, "<nil>"},
		{"outer change is not saved", false, func(s *SessionScheme, c *router.Context, _ http.ResponseWriter) {
			s.Session(c.Request).Put("k", "listener")
		}, "v"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			scheme, _ := storeScheme(t, true)
			listener := func(next router.HandlerFunc) router.HandlerFunc {
				return func(c *router.Context) error {
					if c.Request.URL.Path == "/put" {
						c.BeforeCommit(func(_ int, w http.ResponseWriter) { tt.change(scheme, c, w) })
					}
					return next(c)
				}
			}
			r := router.New()
			if !tt.inside {
				r.Use(listener)
			}
			r.Use(scheme.SessionMiddleware())
			if tt.inside {
				r.Use(listener)
			}
			r.Get("/visit", func(c *router.Context) error { return c.NoContent() })
			r.Get("/put", func(c *router.Context) error {
				scheme.Session(c.Request).Put("k", "v")
				return c.String(http.StatusOK, "ok")
			})
			r.Get("/read", func(c *router.Context) error {
				return c.String(http.StatusOK, fmt.Sprint(scheme.Session(c.Request).Get("k")))
			})
			cookie := visit(t, r)
			req := httptest.NewRequest(http.MethodGet, "/put", nil)
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if n := len(sessionLines(rec)); n > 1 {
				t.Fatalf("response carries %d session cookies, want at most 1", n)
			}
			if got := readStored(t, r, cookie); got != tt.want {
				t.Fatalf("the next request read %q, want %q", got, tt.want)
			}
		})
	}
}

// When a listener of an inner middleware panics, the session's listener
// has not run yet (it is the outermost, so it runs last): it runs with the
// boundary's 500, so the session is saved with that answer, as it is when
// the handler itself panics. The cookie is on the 500, the change is
// persisted, and the request's queue is settled and closed.
func TestSessionMiddleware_InnerListenerPanicStillSaves(t *testing.T) {
	for _, serverSide := range []bool{false, true} {
		t.Run(fmt.Sprintf("server=%v", serverSide), func(t *testing.T) {
			scheme, _ := storeScheme(t, serverSide)
			settled, lateAccepted := 0, true
			r := router.New()
			r.SetErrorHandler(func(c *router.Context, _ error, _ router.ErrorInfo) {
				c.Response.WriteHeader(http.StatusInternalServerError)
			})
			// Outside the session middleware: sees the request leave it,
			// with the save done.
			r.Use(func(next router.HandlerFunc) router.HandlerFunc {
				return func(c *router.Context) error {
					if c.Request.URL.Path == "/put" {
						c.BeforeCommit(func(int, http.ResponseWriter) {
							lateAccepted = QueueAfterSessionSave(c.Request, func(http.ResponseWriter) {})
						})
					}
					return next(c)
				}
			})
			r.Use(scheme.SessionMiddleware())
			r.Use(func(next router.HandlerFunc) router.HandlerFunc {
				return func(c *router.Context) error {
					if c.Request.URL.Path == "/put" {
						c.BeforeCommit(func(int, http.ResponseWriter) { panic("listener exploded") })
					}
					return next(c)
				}
			})
			r.Get("/put", func(c *router.Context) error {
				scheme.Session(c.Request).Put("k", "v")
				if !QueueAfterSessionSave(c.Request, func(http.ResponseWriter) { settled++ }) {
					t.Error("the handler's write behind the save was refused")
				}
				return c.String(http.StatusOK, "ok")
			})
			r.Get("/read", func(c *router.Context) error {
				return c.String(http.StatusOK, fmt.Sprint(scheme.Session(c.Request).Get("k")))
			})
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/put", nil))
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", rec.Code)
			}
			var cookie *http.Cookie
			for _, c := range rec.Result().Cookies() {
				if c.Name == "vel_session" {
					cookie = c
				}
			}
			if cookie == nil {
				t.Fatalf("the 500 carries no session cookie: %v", rec.Result().Header)
			}
			if got := readStored(t, r, cookie); got != "v" {
				t.Fatalf("the next request read %q, want the saved value", got)
			}
			if settled != 1 {
				t.Fatalf("the write queued behind the save ran %d times, want 1", settled)
			}
			if lateAccepted {
				t.Fatal("a write queued after the save was accepted: the queue is not closed")
			}
		})
	}
}

// Every kind of Context saves the session exactly once, whatever the
// handler does, and the session cookie is in the response that is
// committed (a takeover delivers none).
func TestSessionMiddleware_SavesOnceOnEveryKindOfContext(t *testing.T) {
	type outcome struct {
		rec *httptest.ResponseRecorder
	}
	behaviours := map[string]router.HandlerFunc{
		"body":    func(c *router.Context) error { return c.String(http.StatusOK, "ok") },
		"nothing": func(c *router.Context) error { return nil },
		"error":   func(c *router.Context) error { return fmt.Errorf("failed") },
		"hijack": func(c *router.Context) error {
			conn, _, err := c.Response.(http.Hijacker).Hijack()
			if err != nil {
				return err
			}
			return conn.Close()
		},
	}
	newWriter := func() (*hijackingResponseWriter, *httptest.ResponseRecorder) {
		rec := httptest.NewRecorder()
		return &hijackingResponseWriter{ResponseWriter: rec}, rec
	}
	req := func() *http.Request { return httptest.NewRequest(http.MethodGet, "/x", nil) }
	viaRouter := func(mws ...func(*SessionScheme) router.MiddlewareFunc) func(*SessionScheme, router.HandlerFunc) outcome {
		return func(scheme *SessionScheme, h router.HandlerFunc) outcome {
			r := router.New()
			for _, mw := range mws {
				r.Use(mw(scheme))
			}
			r.Get("/x", h)
			w, rec := newWriter()
			r.ServeHTTP(w, req())
			return outcome{rec}
		}
	}
	session := func(s *SessionScheme) router.MiddlewareFunc { return s.SessionMiddleware() }
	timeout := func(*SessionScheme) router.MiddlewareFunc { return router.Timeout(time.Minute) }
	kinds := map[string]struct {
		run      func(*SessionScheme, router.HandlerFunc) outcome
		noHijack bool
	}{
		"router":         {run: viaRouter(session)},
		"TimeoutOutside": {run: viaRouter(session, timeout), noHijack: true},
		"TimeoutInside":  {run: viaRouter(timeout, session), noHijack: true},
		"Wrap": {run: func(scheme *SessionScheme, h router.HandlerFunc) outcome {
			w, rec := newWriter()
			router.Wrap(scheme.SessionMiddleware()(h))(w, req())
			return outcome{rec}
		}},
		"NewContext": {run: func(scheme *SessionScheme, h router.HandlerFunc) outcome {
			w, rec := newWriter()
			_ = scheme.SessionMiddleware()(h)(router.NewContext(w, req()))
			return outcome{rec}
		}},
		"NewTestContext": {run: func(scheme *SessionScheme, h router.HandlerFunc) outcome {
			c, rec := router.NewTestContext(http.MethodGet, "/x")
			_ = scheme.SessionMiddleware()(h)(c)
			return outcome{rec}
		}, noHijack: true},
		// A Context built over the router's own writer, inside a route:
		// the registration binds that writer and the router finalizes it.
		"NewContextOverRouterWriter": {run: func(scheme *SessionScheme, h router.HandlerFunc) outcome {
			r := router.New()
			r.Get("/x", func(c *router.Context) error {
				return scheme.SessionMiddleware()(h)(router.NewContext(c.Response, c.Request))
			})
			w, rec := newWriter()
			r.ServeHTTP(w, req())
			return outcome{rec}
		}},
	}
	for kind, k := range kinds {
		for behaviour, h := range behaviours {
			if behaviour == "hijack" && k.noHijack {
				continue
			}
			t.Run(kind+"/"+behaviour, func(t *testing.T) {
				scheme, _ := storeScheme(t, false)
				saves := 0
				orig := saveSessionFromMiddleware
				saveSessionFromMiddleware = func(g *SessionScheme, w http.ResponseWriter, s contract.Session) error {
					saves++
					return orig(g, w, s)
				}
				defer func() { saveSessionFromMiddleware = orig }()
				out := k.run(scheme, func(c *router.Context) error {
					scheme.Session(c.Request).Put("k", "v")
					return h(c)
				})
				if saves != 1 {
					t.Fatalf("the session was saved %d times, want 1", saves)
				}
				if behaviour == "hijack" {
					return
				}
				if n := len(sessionLines(out.rec)); n != 1 {
					t.Fatalf("the committed response carries %d session cookies, want 1: %v", n, out.rec.Result().Header)
				}
			})
		}
	}
}

// taggedWriter is a writer passed by value whose type cannot be compared:
// comparing two interface values holding it panics.
type taggedWriter struct {
	http.ResponseWriter
	tags map[string]string
}

// A middleware ahead of the session's may leave any writer in the Context,
// one of an uncomparable type included: the session middleware decides
// where it saves without comparing writers, serves the request and saves
// once.
func TestSessionMiddleware_UncomparableWriterInTheContext(t *testing.T) {
	scheme, _ := storeScheme(t, false)
	r := router.New()
	failures := 0
	r.SetErrorHandler(func(_ *router.Context, err error, _ router.ErrorInfo) {
		failures++
		t.Errorf("the request failed: %v", err)
	})
	r.Use(func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			c.Response = taggedWriter{ResponseWriter: c.Response, tags: map[string]string{}}
			return next(c)
		}
	})
	r.Use(scheme.SessionMiddleware())
	r.Get("/x", func(c *router.Context) error {
		scheme.Session(c.Request).Put("k", "v")
		return c.String(http.StatusOK, "ok")
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	// The handler commits before the middleware decides, so a panic in
	// the decision shows as a reported failure, not as another status.
	if rec.Code != http.StatusOK || failures != 0 {
		t.Fatalf("status = %d, reported failures = %d; want 200 and none (comparing the writers panics)", rec.Code, failures)
	}
	if n := len(sessionLines(rec)); n != 1 {
		t.Fatalf("response carries %d session cookies, want 1", n)
	}
}

// On a built Context another listener registered ahead of the session
// middleware: the commit owner is already bound when the middleware
// registers, and still nothing finalizes the Context, so a handler that
// changes the session and writes nothing is saved as it returns.
func TestSessionMiddleware_BuiltContextBoundByAnEarlierListener(t *testing.T) {
	builders := map[string]func() (*router.Context, *httptest.ResponseRecorder){
		"NewTestContext": func() (*router.Context, *httptest.ResponseRecorder) {
			return router.NewTestContext(http.MethodGet, "/x")
		},
		"NewContext": func() (*router.Context, *httptest.ResponseRecorder) {
			rec := httptest.NewRecorder()
			return router.NewContext(rec, httptest.NewRequest(http.MethodGet, "/x", nil)), rec
		},
	}
	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			scheme, _ := storeScheme(t, false)
			c, rec := build()
			if !c.BeforeCommit(func(int, http.ResponseWriter) {}) {
				t.Fatal("the earlier registration was refused")
			}
			err := scheme.SessionMiddleware()(func(c *router.Context) error {
				scheme.Session(c.Request).Put("k", "v")
				return nil
			})(c)
			if err != nil {
				t.Fatal(err)
			}
			if n := len(sessionLines(rec)); n != 1 {
				t.Fatalf("response carries %d session cookies, want 1", n)
			}
		})
	}
}

// forwardingWriter is an application writer that forwards Hijack and says
// nothing about commitment.
type forwardingWriter struct{ http.ResponseWriter }

func (f *forwardingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return f.ResponseWriter.(http.Hijacker).Hijack()
}

// A middleware ahead of the session's leaves its own writer in the
// Context, one that forwards Hijack and reports no commitment. A handler
// that takes the connection over through it still has its session saved
// when it returns.
func TestSessionMiddleware_TakeoverThroughAnApplicationWriter(t *testing.T) {
	scheme, _ := storeScheme(t, true)
	wrap := func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			c.Response = &forwardingWriter{c.Response}
			return next(c)
		}
	}
	r := sessionApp(t, scheme, func(c *router.Context) error {
		conn, _, err := c.Response.(http.Hijacker).Hijack()
		if err != nil {
			return err
		}
		return conn.Close()
	}, wrap)
	cookie := visit(t, r)
	req := httptest.NewRequest(http.MethodGet, "/put", nil)
	req.AddCookie(cookie)
	r.ServeHTTP(&hijackingResponseWriter{ResponseWriter: httptest.NewRecorder()}, req)
	if got := readStored(t, r, cookie); got != "v" {
		t.Fatalf("after the takeover the next request read %q, want the saved value", got)
	}
}

// A handler changes a server-side session, takes the connection over and
// then panics. The takeover ran no listener and the panic skips everything
// after the handler call, so the save has to be on the way out of the
// middleware whichever way the handler leaves it.
func TestSessionMiddleware_TakeoverThenPanicStillSaves(t *testing.T) {
	scheme, _ := storeScheme(t, true)
	r := sessionApp(t, scheme, func(c *router.Context) error {
		conn, _, err := c.Response.(http.Hijacker).Hijack()
		if err != nil {
			return err
		}
		_ = conn.Close()
		panic("handler exploded")
	})
	cookie := visit(t, r)

	req := httptest.NewRequest(http.MethodGet, "/put", nil)
	req.AddCookie(cookie)
	func() {
		defer func() { _ = recover() }()
		r.ServeHTTP(&hijackingResponseWriter{ResponseWriter: httptest.NewRecorder()}, req)
	}()
	if got := readStored(t, r, cookie); got != "v" {
		t.Fatalf("after a takeover and a panic the next request read %q, want the saved value", got)
	}
}

// headerPanicsAfterTakeover is a writer whose Header panics once the
// connection was taken over, so the session save made on the way out of
// the middleware panics.
type headerPanicsAfterTakeover struct {
	hijackingResponseWriter
}

func (w *headerPanicsAfterTakeover) Header() http.Header {
	if w.hijacked.Load() {
		panic("save exploded")
	}
	return w.ResponseWriter.Header()
}

// The save on the way out of the middleware recovers nothing. When the
// handler panicked and that save panics too, Go keeps the later panic: the
// save's value is what a recover further out gets, and the handler's is no
// longer reachable from it. An http.ErrAbortHandler from the handler with
// a save that returns goes on as itself.
func TestSessionMiddleware_PanicInTheExitSaveReplacesTheHandlersPanic(t *testing.T) {
	takeOver := func(c *router.Context) {
		conn, _, err := c.Response.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("Hijack: %v", err)
			return
		}
		_ = conn.Close()
	}
	run := func(w http.ResponseWriter, raise any) (got any) {
		scheme, _ := storeScheme(t, false)
		h := scheme.SessionMiddleware()(func(c *router.Context) error {
			scheme.Session(c.Request).Put("k", "v")
			takeOver(c)
			panic(raise)
		})
		defer func() { got = recover() }()
		router.Wrap(h)(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		return nil
	}

	w := &headerPanicsAfterTakeover{}
	w.ResponseWriter = httptest.NewRecorder()
	if got := run(w, "handler exploded"); got != "save exploded" {
		t.Fatalf("recovered %v, want the save's panic, which replaces the handler's", got)
	}
	if got := run(&hijackingResponseWriter{ResponseWriter: httptest.NewRecorder()}, http.ErrAbortHandler); got != http.ErrAbortHandler {
		t.Fatalf("recovered %v, want http.ErrAbortHandler passed on unchanged", got)
	}
}

// A handler changes the session and panics where nothing will run the
// save's listener: inside router.Timeout, where none can register, and on
// a Context built by NewContext, which nothing finalizes. The save made on
// the way out of the middleware holds the change in both.
func TestSessionMiddleware_HandlerPanicWithNoListenerToRunStillSaves(t *testing.T) {
	change := func(scheme *SessionScheme) router.HandlerFunc {
		return func(c *router.Context) error {
			scheme.Session(c.Request).Put("k", "v")
			panic("handler exploded")
		}
	}
	cases := map[string]func(scheme *SessionScheme, req *http.Request){
		"inside Timeout": func(scheme *SessionScheme, req *http.Request) {
			r := router.New()
			r.Use(router.Timeout(time.Minute))
			r.Use(scheme.SessionMiddleware())
			r.Get("/put", change(scheme))
			r.ServeHTTP(httptest.NewRecorder(), req)
		},
		"built Context": func(scheme *SessionScheme, req *http.Request) {
			_ = scheme.SessionMiddleware()(change(scheme))(router.NewContext(httptest.NewRecorder(), req))
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			scheme, _ := storeScheme(t, true)
			reader := sessionApp(t, scheme, func(c *router.Context) error { return c.NoContent() })
			cookie := visit(t, reader)
			req := httptest.NewRequest(http.MethodGet, "/put", nil)
			req.AddCookie(cookie)
			func() {
				defer func() { _ = recover() }()
				run(scheme, req)
			}()
			if got := readStored(t, reader, cookie); got != "v" {
				t.Fatalf("after the handler's panic the next request read %q, want the saved value", got)
			}
		})
	}
}
