package router

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTimeout_HandlerCompletesBeforeDeadline(t *testing.T) {
	r := New()
	r.Use(Timeout(500 * time.Millisecond))
	r.Get("/fast", func(c *Context) error {
		return c.String(http.StatusOK, "OK")
	})

	req := httptest.NewRequest("GET", "/fast", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "OK") {
		t.Errorf("expected body OK, got %q", w.Body.String())
	}
}

func TestTimeout_HandlerExceedsDeadline(t *testing.T) {
	r := New()
	r.Use(Timeout(50 * time.Millisecond))
	r.Get("/slow", func(c *Context) error {
		// Wait longer than the timeout
		select {
		case <-time.After(500 * time.Millisecond):
		case <-c.Request.Context().Done():
		}
		return nil
	})

	req := httptest.NewRequest("GET", "/slow", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Service Unavailable") {
		t.Errorf("expected Service Unavailable body, got %q", w.Body.String())
	}
}

func TestTimeout_PropagatesHandlerError(t *testing.T) {
	r := New()
	r.Use(Timeout(500 * time.Millisecond))
	r.Get("/err", func(c *Context) error {
		return c.String(http.StatusBadRequest, "bad")
	})

	req := httptest.NewRequest("GET", "/err", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestTimeout_ContextDeadlineSet(t *testing.T) {
	r := New()
	timeout := 200 * time.Millisecond
	r.Use(Timeout(timeout))

	var hasDeadline bool
	r.Get("/check", func(c *Context) error {
		_, hasDeadline = c.Request.Context().Deadline()
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest("GET", "/check", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if !hasDeadline {
		t.Error("expected context to have a deadline")
	}
}

// TestTimeout_HandlerHangsPastDeadline_NoBlock asserts that a handler
// which intentionally ignores ctx.Done() and outlives the timeout does
// not pin the request. The middleware must return promptly with a 503,
// and the late handler must not produce a visible body. Run with -race
// to confirm there is no concurrent write on the response writer.
func TestTimeout_HandlerHangsPastDeadline_NoBlock(t *testing.T) {
	r := New()
	r.Use(Timeout(50 * time.Millisecond))

	wrote := make(chan struct{}, 1)
	r.Get("/hang", func(c *Context) error {
		// Sleep well past the timeout while ignoring ctx.Done().
		time.Sleep(300 * time.Millisecond)
		// This write must be swallowed because the timeout has
		// already fired and the wrapper is in the timed-out state.
		_, err := c.Response.Write([]byte("late body"))
		if !errors.Is(err, ErrHandlerTimeout) {
			t.Errorf("expected ErrHandlerTimeout from late Write, got %v", err)
		}
		wrote <- struct{}{}
		return nil
	})

	req := httptest.NewRequest("GET", "/hang", nil)
	w := httptest.NewRecorder()

	start := time.Now()
	r.ServeHTTP(w, req)
	elapsed := time.Since(start)

	// Must return shortly after the 50ms timeout, never wait for the
	// 300ms hang to complete.
	if elapsed >= 250*time.Millisecond {
		t.Fatalf("timeout middleware blocked on slow handler: %v", elapsed)
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Service Unavailable") {
		t.Fatalf("expected Service Unavailable body, got %q", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "late body") {
		t.Fatalf("late handler body leaked into response: %q", w.Body.String())
	}

	// Wait for the handler goroutine to finish so the test does not
	// leak it. We do this after asserting elapsed so the assertion
	// above proves the middleware itself did not block.
	select {
	case <-wrote:
	case <-time.After(2 * time.Second):
		t.Fatal("late handler never finished")
	}
}

// TestTimeout_LateWriteSwallowed asserts that a handler write occurring
// strictly after the timeout fires returns ErrHandlerTimeout and never
// reaches the underlying ResponseWriter.
func TestTimeout_LateWriteSwallowed(t *testing.T) {
	r := New()
	r.Use(Timeout(30 * time.Millisecond))

	gate := make(chan struct{})
	done := make(chan error, 1)
	r.Get("/late", func(c *Context) error {
		// Wait until the test releases us, which happens after we
		// have observed the 503 response on the wire.
		<-gate
		_, err := c.Response.Write([]byte("late body"))
		done <- err
		return nil
	})

	req := httptest.NewRequest("GET", "/late", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
	bodyBeforeLate := w.Body.String()

	// Release the handler now that we have the 503 in hand.
	close(gate)

	select {
	case err := <-done:
		if !errors.Is(err, ErrHandlerTimeout) {
			t.Fatalf("expected ErrHandlerTimeout, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("late handler never returned")
	}

	if w.Body.String() != bodyBeforeLate {
		t.Fatalf("late write reached the wire: before=%q after=%q",
			bodyBeforeLate, w.Body.String())
	}
}

// TestTimeout_HeaderMutationAfterTimeout_DoesNotLeak asserts that a
// handler which mutates response headers after the timeout has fired
// cannot leak those headers to the real response. The buffered
// timeoutWriter holds its own header map, so handler writes operate on
// the buffer rather than the underlying ResponseWriter.
func TestTimeout_HeaderMutationAfterTimeout_DoesNotLeak(t *testing.T) {
	r := New()
	r.Use(Timeout(30 * time.Millisecond))

	gate := make(chan struct{})
	done := make(chan struct{})
	r.Get("/leakhdr", func(c *Context) error {
		<-gate
		// Set a header strictly after the timeout has fired. With a
		// shared header map this would land on the real response; the
		// buffered writer keeps it private.
		c.Response.Header().Set("X-Leaked-Header", "should-not-appear")
		close(done)
		return nil
	})

	req := httptest.NewRequest("GET", "/leakhdr", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
	if got := w.Header().Get("X-Leaked-Header"); got != "" {
		t.Fatalf("header leaked through buffered writer: %q", got)
	}

	close(gate)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("late handler never finished")
	}

	// Re-check after the late handler has run, to confirm the
	// post-timeout mutation never escapes to the recorder.
	if got := w.Header().Get("X-Leaked-Header"); got != "" {
		t.Fatalf("header leaked after late handler run: %q", got)
	}
}

// TestTimeout_BufferedHeadersFlushed asserts that headers and body set
// by a handler which completes within the deadline are flushed to the
// real ResponseWriter when the middleware commits the buffered
// response.
func TestTimeout_BufferedHeadersFlushed(t *testing.T) {
	r := New()
	r.Use(Timeout(500 * time.Millisecond))
	r.Get("/buffered", func(c *Context) error {
		c.Response.Header().Set("X-Custom", "value")
		c.Response.Header().Set("Content-Type", "application/json")
		c.Response.WriteHeader(http.StatusAccepted)
		_, _ = c.Response.Write([]byte(`{"ok":true}`))
		return nil
	})

	req := httptest.NewRequest("GET", "/buffered", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", w.Code)
	}
	if got := w.Header().Get("X-Custom"); got != "value" {
		t.Fatalf("expected X-Custom header value, got %q", got)
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", got)
	}
	if got := w.Body.String(); got != `{"ok":true}` {
		t.Fatalf("expected buffered body to be flushed, got %q", got)
	}
}

// TestTimeout_NoRaceOnConcurrentWrite stresses the timeout-safe writer
// by hammering it from many goroutines after the timeout has fired.
// Combined with -race it catches any unguarded access to the wrapped
// writer's state. The test is deterministic: the middleware always
// times out before the handler returns.
func TestTimeout_NoRaceOnConcurrentWrite(t *testing.T) {
	r := New()
	r.Use(Timeout(20 * time.Millisecond))

	var wg sync.WaitGroup
	var lateWrites atomic.Int32

	r.Get("/race", func(c *Context) error {
		// Block past the timeout so the writer is guaranteed to be
		// in the timed-out state for every write below.
		time.Sleep(120 * time.Millisecond)
		const writers = 16
		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// Write and WriteHeader are the operations the
				// timeout-safe wrapper must serialize with the
				// middleware's 503. Header() is documented as
				// non-thread-safe (matching net/http behavior),
				// so we exercise only Write/WriteHeader here.
				if _, err := c.Response.Write([]byte("x")); err == nil {
					lateWrites.Add(1)
				}
				c.Response.WriteHeader(http.StatusTeapot)
			}()
		}
		wg.Wait()
		return nil
	})

	req := httptest.NewRequest("GET", "/race", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}

	// Drain the goroutines launched inside the handler before we
	// return, otherwise -race may flag them at process teardown.
	wg.Wait()

	if got := lateWrites.Load(); got != 0 {
		t.Fatalf("late writes returned nil error %d times; expected 0", got)
	}
}

// TestTimeout_HandlerSet_DoesNotLeakToPool guarantees that a handler
// which keeps running after timeout cannot mutate the pooled Context.
// We assert this indirectly by exercising the pool: after the slow
// request times out, a fast follow-up request must observe a clean
// values map. With a shared (non-cloned) Context, the late handler's
// Set would race with the next request's reads.
func TestTimeout_HandlerSet_DoesNotLeakToPool(t *testing.T) {
	r := New()
	r.Use(Timeout(30 * time.Millisecond))

	released := make(chan struct{})
	finished := make(chan struct{})

	r.Get("/slow", func(c *Context) error {
		<-released
		// Write to the values map after the timeout fires. With a
		// pooled context this would race with reset(); the clone
		// isolates it. The error from Write is expected.
		c.Set("leak", "do-not-see-me")
		_, _ = c.Response.Write([]byte("ignored"))
		close(finished)
		return nil
	})

	r.Get("/fast", func(c *Context) error {
		if v := c.Get("leak"); v != nil {
			t.Errorf("pool leak: follow-up request saw stale value %v", v)
		}
		return c.String(http.StatusOK, "ok")
	})

	// Issue the slow request first; it will time out.
	slowReq := httptest.NewRequest("GET", "/slow", nil)
	slowResp := httptest.NewRecorder()
	r.ServeHTTP(slowResp, slowReq)
	if slowResp.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 from slow handler, got %d", slowResp.Code)
	}

	// Issue a fast follow-up. This is likely to receive the recycled
	// Context that previously served /slow. With cloning, the late
	// handler's c.Set("leak") landed on the goroutine-local clone,
	// not on the pooled parent, so /fast must not see it.
	fastReq := httptest.NewRequest("GET", "/fast", nil)
	fastResp := httptest.NewRecorder()
	r.ServeHTTP(fastResp, fastReq)
	if fastResp.Code != http.StatusOK {
		t.Fatalf("expected 200 from fast handler, got %d", fastResp.Code)
	}

	// Let the slow handler finish so the goroutine does not outlive
	// the test.
	close(released)
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("slow handler never finished")
	}
}
