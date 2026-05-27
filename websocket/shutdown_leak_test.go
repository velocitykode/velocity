package websocket

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	testsync "github.com/velocitykode/velocity/testing"
)

// TestShutdown_NoGoroutineLeakAt300Clients reproduces audit finding D-02.
//
// The unregister channel is buffered at cap 256 and only drained by Server.run,
// which exits as soon as stopChan closes. Pre-fix, readPump's defer did a bare
// `c.Server.unregister <- c` with no select on stopChan, so once Shutdown had
// closed every live connection (kicking readPump out of ReadJSON), the first
// 256 readPumps could enqueue and exit, but any beyond that blocked forever on
// the channel send. Their wg.Done deferred behind the send never ran, so
// Shutdown's wg.Wait returned ctx.Err() and left N-256 goroutines permanently
// pinned.
//
// With the fix (select on stopChan), every readPump exits cleanly when stopChan
// closes, so the post-shutdown goroutine count returns to baseline.
func TestShutdown_NoGoroutineLeakAt300Clients(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping leak test in short mode")
	}

	const totalClients = 300 // > 256 (unregister buffer cap)

	s := New(DefaultConfig())
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ts := httptest.NewServer(http.HandlerFunc(s.HandleConnection))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	// Connect clients in parallel to keep wallclock down.
	conns := make([]*websocket.Conn, totalClients)
	var wg sync.WaitGroup
	wg.Add(totalClients)
	for i := 0; i < totalClients; i++ {
		go func(idx int) {
			defer wg.Done()
			ws, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
			if err != nil {
				t.Errorf("client %d dial: %v", idx, err)
				return
			}
			conns[idx] = ws
			// Drain welcome so the connection settles.
			var welcome Message
			_ = ws.ReadJSON(&welcome)
		}(i)
	}
	wg.Wait()

	for _, c := range conns {
		if c == nil {
			t.Fatal("a client failed to connect")
		}
	}

	// Wait for the server to register all clients.
	testsync.Eventually(t, func() bool {
		return s.GetStats().ConnectedClients == int64(totalClients)
	}, 5*time.Second, "all clients registered server-side")

	// Capture a baseline that already includes per-client read/write pumps so
	// the post-shutdown delta isolates leaked pumps only.
	beforeShutdown := runtime.NumGoroutine()

	// Initiate shutdown with a generous deadline. The fix lets every pump
	// exit promptly; pre-fix this hits ctx.Done with N-256 pumps still blocked.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown returned error (likely indicates the leak bug): %v", err)
	}

	// Close client-side conns so the dial goroutines linked to the dialer
	// don't skew the count.
	for _, c := range conns {
		_ = c.Close()
	}

	// Allow some scheduler slack for exited goroutines to be reaped.
	deadline := time.Now().Add(3 * time.Second)
	var after int
	for time.Now().Before(deadline) {
		runtime.GC()
		after = runtime.NumGoroutine()
		// We expect a sizable decrease: every per-client pump should have exited.
		// 2 pumps per client * totalClients = 600 pumps in flight pre-shutdown.
		// Pre-fix leaks ~ (totalClients - 256) read pumps == 44 goroutines.
		// Allow up to 20 stragglers (httptest connections, gorilla internals,
		// goroutine scheduler noise).
		if beforeShutdown-after >= totalClients {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	leaked := after - (beforeShutdown - 2*totalClients) // expected: pumps fully gone
	if leaked > 20 {
		t.Fatalf("goroutine leak after Shutdown: before=%d after=%d leaked=%d (>20 tolerance)",
			beforeShutdown, after, leaked)
	}
}

// TestReadPump_ExitsOnStopChan exercises the select-on-stopChan branch in
// readPump's defer directly: with no run loop running, the unregister channel
// is never drained. If readPump's defer used a bare send, it would block
// forever. With the select on stopChan, closing stopChan must release the pump.
//
// We construct a Server without calling Start so there is no run loop, then
// call readPump on a client whose Conn is already closed (so ReadJSON returns
// immediately and the defer fires).
func TestReadPump_ExitsOnStopChan(t *testing.T) {
	s := New(DefaultConfig())

	// Saturate the unregister buffer so any send blocks.
	for i := 0; i < cap(s.unregister); i++ {
		s.unregister <- &Client{ID: "filler"}
	}

	// Bring up a dummy WS connection. We need a real *websocket.Conn so
	// readPump's SetReadDeadline/SetPongHandler/ReadJSON path works, but we
	// close it immediately so ReadJSON returns and the defer fires.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := s.upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		// Hand the conn to a client and run readPump in this goroutine so we
		// can wait for it. The client is NOT registered with the server's
		// clients map and there is NO run loop draining s.unregister.
		client := &Client{
			ID:       "leaktest",
			Conn:     conn,
			Send:     make(chan Message, 1),
			Server:   s,
			Groups:   make(map[string]bool),
			Metadata: make(map[string]interface{}),
		}

		done := make(chan struct{})
		go func() {
			client.readPump()
			close(done)
		}()

		// Close the conn to force ReadJSON to return so the defer runs.
		conn.Close()

		// Close stopChan from another goroutine: the defer should select on
		// it and exit even though no one is draining s.unregister.
		go func() {
			time.Sleep(50 * time.Millisecond)
			close(s.stopChan)
		}()

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Errorf("readPump did not exit within 2s after stopChan closed; unregister send is blocking")
		}
	}))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	// Give the handler time to complete its assertions.
	time.Sleep(2500 * time.Millisecond)
}
