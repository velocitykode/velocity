package velocity

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/router"
)

// TestShutdown_DrainsInFlightRequests verifies the graceful shutdown
// contract: SIGTERM-equivalent (Shutdown) stops accepting new connections,
// lets in-flight requests complete, closes subsystems, and returns within
// the deadline. Regression gate for item 5 of the pre-1.0 readiness audit.
func TestShutdown_DrainsInFlightRequests(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	if err := a.bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	// Handler signals "I'm in-flight" on entry, then sleeps long enough to
	// still be in-flight when Shutdown is called. The started channel is
	// a barrier the test uses to delay Shutdown until every request has
	// actually been accepted — without that, a sleep-based wait races CI
	// scheduler jitter and drops connections that were not yet accepted
	// (the shutdown contract is "drain accepted connections," not "drain
	// TCP buffers").
	const N = 100
	started := make(chan struct{}, N)
	var completed int64
	a.Router.Get("/slow", func(c *router.Context) error {
		started <- struct{}{}
		time.Sleep(200 * time.Millisecond)
		atomic.AddInt64(&completed, 1)
		c.Response.WriteHeader(http.StatusOK)
		_, _ = c.Response.Write([]byte("done"))
		return nil
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()

	a.server = &http.Server{Handler: a.Router}
	a.Router.Freeze()

	serveErr := make(chan error, 1)
	go func() {
		if err := a.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			serveErr <- err
		}
		close(serveErr)
	}()

	// Baseline goroutines AFTER the server's Serve goroutine and its
	// acceptors have spawned and stabilized.
	waitForGoroutinesToSettle(t, time.Second)
	baselineGoroutines := runtime.NumGoroutine()

	var wg sync.WaitGroup
	statuses := make([]int, N)
	errs := make([]error, N)

	client := &http.Client{Timeout: 10 * time.Second}
	url := fmt.Sprintf("http://%s/slow", addr)

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, err := client.Get(url)
			if err != nil {
				errs[i] = err
				return
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
			statuses[i] = resp.StatusCode
		}(i)
	}

	// Wait until every request's handler has been accepted and is in
	// its sleep — only then does Shutdown exercise the drain path.
	for i := 0; i < N; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for request %d to enter handler", i+1)
		}
	}

	shutdownStart := time.Now()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}
	shutdownElapsed := time.Since(shutdownStart)

	wg.Wait()

	// (a) All 100 requests completed with 200.
	for i := 0; i < N; i++ {
		if errs[i] != nil {
			t.Errorf("request[%d] errored: %v", i, errs[i])
			continue
		}
		if statuses[i] != http.StatusOK {
			t.Errorf("request[%d] status = %d, want 200", i, statuses[i])
		}
	}
	if got := atomic.LoadInt64(&completed); got != N {
		t.Errorf("handler ran %d times, want %d", got, N)
	}

	// (b) Shutdown returned within the bound. The handler sleep is 200ms;
	// we allow generous slack for CI scheduling jitter before calling
	// the drain itself slow.
	if shutdownElapsed > 3*time.Second {
		t.Errorf("Shutdown took %v; expected < 3s (handler sleep 200ms + CI slack)", shutdownElapsed)
	}

	// (c) Goroutine count returns to baseline. http.Server.Shutdown can
	// leave transient goroutines for a beat; poll until the count settles,
	// then compare against baseline with generous slack for CI jitter.
	waitForGoroutinesToSettle(t, time.Second)
	finalGoroutines := runtime.NumGoroutine()
	if delta := finalGoroutines - baselineGoroutines; delta > 5 {
		t.Errorf("goroutine leak: baseline=%d final=%d delta=%d",
			baselineGoroutines, finalGoroutines, delta)
	}

	// Confirm Serve returned without error.
	select {
	case err, ok := <-serveErr:
		if ok && err != nil {
			t.Errorf("server.Serve returned unexpected error: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("server.Serve did not return after Shutdown")
	}
}
