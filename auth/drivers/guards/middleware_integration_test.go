package guards

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/router"
)

// newRealCookieScheme builds a SessionScheme backed by the production
// CookieStore + AES-256-GCM encryptor, so the integration tests below
// observe real Set-Cookie headers on the wire, not a mock store.
func newRealCookieScheme(t *testing.T) *SessionScheme {
	t.Helper()
	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    strings.Repeat("k", 32),
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	scheme, err := NewSessionScheme(&mockSessionSchemeUserStore{}, auth.SessionConfig{
		Name:     "vel_session",
		Lifetime: 60,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}, enc)
	if err != nil {
		t.Fatalf("NewSessionScheme: %v", err)
	}
	return scheme
}

// newRouterWithSessionMiddleware returns a freshly constructed router
// with the save-at-end session middleware installed. handler is the
// single route at GET / for the test scenario.
func newRouterWithSessionMiddleware(t *testing.T, scheme *SessionScheme, handler router.HandlerFunc) *router.VelocityRouterV2 {
	t.Helper()
	r := router.New()
	r.Use(scheme.SessionMiddleware())
	r.Get("/", handler)
	return r
}

// hasSessionCookie reports whether resp carries a Set-Cookie header for
// the named session cookie. Used by the integration tests to prove the
// pre-commit hook flushed the cookie ahead of the real net/http header
// commit.
func hasSessionCookie(resp *http.Response, name string) (*http.Cookie, bool) {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c, true
		}
	}
	return nil, false
}

// TestSessionMiddleware_Integration_JSONHandlerFlushesCookie is the
// critical-path F3 regression test. A handler that mutates the session
// and then writes a JSON body MUST deliver Set-Cookie to the client.
// Pre-fix, c.JSON committed headers from inside the handler and the
// post-handler save wrote Set-Cookie into already-flushed headers,
// silently dropping it.
func TestSessionMiddleware_Integration_JSONHandlerFlushesCookie(t *testing.T) {
	scheme := newRealCookieScheme(t)
	r := newRouterWithSessionMiddleware(t, scheme, func(c *router.Context) error {
		// Mutate the session, then commit headers via JSON.
		s := scheme.getSession(c.Request)
		s.Put("user_id", "u-json")
		return c.JSON(http.StatusOK, map[string]string{"ok": "true"})
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}
	cookie, ok := hasSessionCookie(resp, "vel_session")
	if !ok {
		t.Fatalf("response carries no vel_session Set-Cookie; pre-commit hook did not fire ahead of c.JSON header commit")
	}
	if cookie.Value == "" {
		t.Fatalf("Set-Cookie carried empty value; expected encrypted payload")
	}
}

// TestSessionMiddleware_Integration_RedirectFlushesCookie covers the
// redirect path. http.Redirect calls WriteHeader with 302 from inside
// c.Redirect; without the pre-commit hook the cookie would be lost.
func TestSessionMiddleware_Integration_RedirectFlushesCookie(t *testing.T) {
	scheme := newRealCookieScheme(t)
	r := newRouterWithSessionMiddleware(t, scheme, func(c *router.Context) error {
		s := scheme.getSession(c.Request)
		s.Put("redirect", "yes")
		return c.Redirect(http.StatusFound, "/elsewhere")
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	// Don't follow the redirect; we want to inspect the 302 itself.
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d; want 302", resp.StatusCode)
	}
	if _, ok := hasSessionCookie(resp, "vel_session"); !ok {
		t.Fatalf("302 response carries no vel_session Set-Cookie; pre-commit hook did not fire")
	}
}

// TestSessionMiddleware_Integration_EmptyHandlerFlushesCookie covers the
// defer-fallback path: handler mutates the session but returns without
// writing any output. The pre-commit hook never fires; the defer Save
// MUST still flush Set-Cookie.
func TestSessionMiddleware_Integration_EmptyHandlerFlushesCookie(t *testing.T) {
	scheme := newRealCookieScheme(t)
	r := newRouterWithSessionMiddleware(t, scheme, func(c *router.Context) error {
		s := scheme.getSession(c.Request)
		s.Put("empty", "yes")
		// No JSON, no WriteHeader, no Write.
		return nil
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if _, ok := hasSessionCookie(resp, "vel_session"); !ok {
		t.Fatalf("empty-body response carries no vel_session Set-Cookie; defer-fallback did not fire")
	}
}

// TestSessionMiddleware_Integration_ReadOnlyHandlerNoCookie pins the
// "no work, no header" contract: a handler that only reads an EXISTING
// session (no Put/Flash/Invalidate) MUST NOT trip a Set-Cookie.
// Otherwise every read request would rotate the cookie value and break
// anything keyed by it (CSRF token stores).
//
// Two-request test: first GET mints a fresh session cookie. Second GET
// presents that cookie and only reads, so the second response must
// carry no Set-Cookie.
func TestSessionMiddleware_Integration_ReadOnlyHandlerNoCookie(t *testing.T) {
	scheme := newRealCookieScheme(t)
	r := newRouterWithSessionMiddleware(t, scheme, func(c *router.Context) error {
		s := scheme.getSession(c.Request)
		// Two distinct routes? No: one handler, mutation gated by
		// query param so we can drive both states from one test.
		if c.Request.URL.Query().Get("write") == "1" {
			s.Put("seed", "yes")
		} else {
			_ = s.Get("seed") // pure read
		}
		return c.JSON(http.StatusOK, map[string]string{"ok": "true"})
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	// First request: write. Expect Set-Cookie.
	resp1, err := http.Get(srv.URL + "/?write=1")
	if err != nil {
		t.Fatalf("seed GET: %v", err)
	}
	resp1.Body.Close()
	seedCookie, ok := hasSessionCookie(resp1, "vel_session")
	if !ok {
		t.Fatalf("seed request did not issue Set-Cookie; test setup broken")
	}

	// Second request: read-only, replays the seed cookie.
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.AddCookie(seedCookie)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("read GET: %v", err)
	}
	defer resp2.Body.Close()

	if _, ok := hasSessionCookie(resp2, "vel_session"); ok {
		t.Fatalf("read-only handler unexpectedly emitted vel_session Set-Cookie on a request that only Get()s")
	}
}

// TestSessionMiddleware_Integration_DestroyedSessionEmitsDelete pins the
// Invalidate path: a handler that destroys the session MUST emit a
// MaxAge=-1 cookie so the browser drops the session id immediately.
func TestSessionMiddleware_Integration_DestroyedSessionEmitsDelete(t *testing.T) {
	scheme := newRealCookieScheme(t)
	r := newRouterWithSessionMiddleware(t, scheme, func(c *router.Context) error {
		s := scheme.getSession(c.Request)
		_ = s.Invalidate()
		return c.JSON(http.StatusOK, map[string]string{"ok": "destroyed"})
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	cookie, ok := hasSessionCookie(resp, "vel_session")
	if !ok {
		t.Fatalf("destroyed session did not emit vel_session Set-Cookie")
	}
	if cookie.MaxAge >= 0 {
		t.Fatalf("destroyed session Set-Cookie MaxAge = %d; want < 0 to instruct browser deletion", cookie.MaxAge)
	}
}

// hijackingResponseWriter mimics a net/http connection that supports
// Hijacker. Used to assert the responseWriter forwards Hijack through
// the wrapper layer even with the BeforeFirstWrite hook installed.
type hijackingResponseWriter struct {
	http.ResponseWriter
	hijacked atomic.Bool
}

func (h *hijackingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.hijacked.Store(true)
	// Return a dummy pipe so the caller does not deref nil.
	c1, c2 := net.Pipe()
	_ = c2 // discard the other end; the test does not read from it
	return c1, bufio.NewReadWriter(bufio.NewReader(c1), bufio.NewWriter(c1)), nil
}

// TestSessionMiddleware_Integration_HijackerForwardsThroughWrapper
// confirms the BeforeFirstWrite hook does not break WebSocket-style
// upgrades. A handler that type-asserts c.Response.(http.Hijacker)
// MUST still get a working Hijack call.
func TestSessionMiddleware_Integration_HijackerForwardsThroughWrapper(t *testing.T) {
	scheme := newRealCookieScheme(t)

	// Build the wrapper manually so we control the underlying writer.
	// We can't easily use httptest.NewServer because the test client
	// would race the hijack; instead, drive ServeHTTP directly.
	inner := &hijackingResponseWriter{ResponseWriter: httptest.NewRecorder()}

	r := router.New()
	hijackerSeen := atomic.Bool{}
	r.Use(scheme.SessionMiddleware())
	r.Get("/ws", func(c *router.Context) error {
		s := scheme.getSession(c.Request)
		s.Put("ws-user", "u-1")
		// Now simulate a websocket upgrade: type-assert Hijacker and
		// invoke Hijack. The responseWriter wrapper MUST forward.
		h, ok := c.Response.(http.Hijacker)
		if !ok {
			t.Errorf("c.Response does not implement http.Hijacker; wrapper broke the contract")
			return nil
		}
		conn, _, err := h.Hijack()
		if err != nil {
			t.Errorf("Hijack returned err=%v", err)
			return nil
		}
		hijackerSeen.Store(true)
		_ = conn.Close()
		return nil
	})

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.ServeHTTP(inner, req)

	if !inner.hijacked.Load() {
		t.Fatal("underlying writer did not see Hijack call")
	}
	if !hijackerSeen.Load() {
		t.Fatal("handler did not observe http.Hijacker assertion success")
	}
}

// TestSessionMiddleware_Integration_ConcurrentRequests asserts the
// pre-commit hook works under concurrent request load: each request must
// get its own session cookie and the wrapper must not deadlock.
func TestSessionMiddleware_Integration_ConcurrentRequests(t *testing.T) {
	scheme := newRealCookieScheme(t)
	r := newRouterWithSessionMiddleware(t, scheme, func(c *router.Context) error {
		s := scheme.getSession(c.Request)
		s.Put("concurrent", c.Request.URL.Query().Get("id"))
		return c.JSON(http.StatusOK, map[string]string{"ok": "true"})
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	const workers = 16
	var wg sync.WaitGroup
	wg.Add(workers)
	errs := make(chan error, workers)

	for i := 0; i < workers; i++ {
		i := i
		go func() {
			defer wg.Done()
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
				srv.URL+"/?id="+strings.Repeat("a", i+1), nil)
			if err != nil {
				errs <- err
				return
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				errs <- err
				return
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				errs <- httpErr(resp.StatusCode)
				return
			}
			if _, ok := hasSessionCookie(resp, "vel_session"); !ok {
				errs <- errNoCookie
				return
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent request: %v", err)
		}
	}
}

type httpErr int

func (e httpErr) Error() string { return http.StatusText(int(e)) }

var errNoCookie = errSentinel("response carries no vel_session Set-Cookie")

type errSentinel string

func (e errSentinel) Error() string { return string(e) }

// TestSessionMiddleware_Integration_FlushFlushesCookie is the F4
// regression test. A handler that mutates the session and then calls
// Flush() (the SSE / streaming pattern) MUST deliver Set-Cookie ahead
// of the flushed body. Go's net/http chunkWriter commits the status
// line + headers on the first Flush call, so without the pre-commit
// hook on responseWriter.Flush the Set-Cookie ends up on the wire AFTER
// the headers have already been emitted and is silently dropped.
//
// We drive the handler against a real httptest.NewServer so Go's
// production response writer is exercised (httptest.ResponseRecorder
// accepts late header writes and would mask the bug).
func TestSessionMiddleware_Integration_FlushFlushesCookie(t *testing.T) {
	scheme := newRealCookieScheme(t)
	r := newRouterWithSessionMiddleware(t, scheme, func(c *router.Context) error {
		// Mutate the session BEFORE any write.
		s := scheme.getSession(c.Request)
		s.Put("sse-user", "u-sse")

		// SSE-style setup: set headers, then write an event frame and
		// Flush. The Flush is the moment Go commits headers; the
		// pre-commit hook MUST fire here so Set-Cookie lands in the
		// same header block.
		c.Response.Header().Set("Content-Type", "text/event-stream")
		c.Response.Header().Set("Cache-Control", "no-cache")
		c.Response.Header().Set("X-Accel-Buffering", "no")

		if _, err := c.Response.Write([]byte("event: hello\ndata: {\"k\":\"v\"}\n\n")); err != nil {
			return err
		}
		flusher, ok := c.Response.(http.Flusher)
		if !ok {
			t.Errorf("c.Response does not implement http.Flusher; cannot exercise F4")
			return nil
		}
		flusher.Flush()
		return nil
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}
	cookie, ok := hasSessionCookie(resp, "vel_session")
	if !ok {
		t.Fatalf("SSE response carries no vel_session Set-Cookie; Flush() did not fire pre-commit hook")
	}
	if cookie.Value == "" {
		t.Fatalf("Set-Cookie carried empty value; expected encrypted payload")
	}

	// Belt-and-braces: also assert the SSE payload made it to the
	// wire so the test proves BOTH paths (the dirty-session save AND
	// the body the handler intended to emit) survived the flush.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), "event: hello") {
		t.Fatalf("SSE payload not delivered; body=%q", string(body))
	}
}

// TestSessionMiddleware_Integration_BareFlushBeforeWriteFiresHook
// pins the corner case: a handler that calls Flush() BEFORE writing any
// body (e.g. to flush early headers for slow-handler keepalive) still
// commits Set-Cookie through the pre-commit hook. Without F4, the bare
// Flush would commit empty headers without firing the hook and the
// downstream Write/return would write into already-committed headers.
func TestSessionMiddleware_Integration_BareFlushBeforeWriteFiresHook(t *testing.T) {
	scheme := newRealCookieScheme(t)
	r := newRouterWithSessionMiddleware(t, scheme, func(c *router.Context) error {
		s := scheme.getSession(c.Request)
		s.Put("bare-flush", "yes")

		// Bare Flush before any Write. Go commits status line + headers
		// here. Pre-commit hook must fire.
		if f, ok := c.Response.(http.Flusher); ok {
			f.Flush()
		}
		// Continuing the response is fine; headers are already
		// committed, but Set-Cookie should already be in them.
		_, _ = c.Response.Write([]byte("ok"))
		return nil
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if _, ok := hasSessionCookie(resp, "vel_session"); !ok {
		t.Fatalf("bare-Flush handler did not emit vel_session Set-Cookie; F4 hook did not fire on Flush()")
	}
}
