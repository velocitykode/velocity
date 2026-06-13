package websocket

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	testsync "github.com/velocitykode/velocity/testing"
)

// dialClient connects a client to the test server and drains the welcome
// message so the connection has settled by the time it returns.
func dialClient(t *testing.T, tsURL string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(tsURL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(tsURL))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	var welcome Message
	_ = ws.ReadJSON(&welcome)
	return ws
}

// TestOnConnect_RunsAfterRegistration is the B49 regression: an OnConnect
// callback that calls JoinGroup must succeed, because the callback now fires on
// the run-loop goroutine after the client is in s.clients. Pre-fix it ran on
// the request goroutine racing handleRegister and saw "client not found".
func TestOnConnect_RunsAfterRegistration(t *testing.T) {
	s := New(DefaultConfig())

	var joinErr atomic.Pointer[error]
	var joined atomic.Bool
	s.OnConnect(func(c *Client) {
		err := s.JoinGroup(c.ID, "room")
		joinErr.Store(&err)
		joined.Store(true)
	})

	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer s.Shutdown(ctx)

	ts := httptest.NewServer(http.HandlerFunc(s.HandleConnection))
	defer ts.Close()

	ws := dialClient(t, ts.URL)
	defer ws.Close()

	testsync.Eventually(t, joined.Load, 5*time.Second, "OnConnect callback ran")

	if p := joinErr.Load(); p == nil || *p != nil {
		t.Fatalf("JoinGroup from OnConnect failed: %v", p)
	}
	// The client must actually be in the group.
	testsync.Eventually(t, func() bool {
		return s.GetGroupCount() == 1 && !s.IsGroupEmpty("room")
	}, 5*time.Second, "client registered into group from OnConnect")
}

// errCapLogger records Error-level log messages for assertions.
type errCapLogger struct {
	mu     sync.Mutex
	errors []string
}

func (l *errCapLogger) Info(string, ...any) {}
func (l *errCapLogger) Warn(string, ...any) {}
func (l *errCapLogger) Error(msg string, _ ...any) {
	l.mu.Lock()
	l.errors = append(l.errors, msg)
	l.mu.Unlock()
}
func (l *errCapLogger) has(substr string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.errors {
		if strings.Contains(e, substr) {
			return true
		}
	}
	return false
}

// TestOnConnect_PanicDoesNotKillRunLoop is the second half of B49: a panicking
// OnConnect callback must be recovered (logged, not propagated) and must not
// take the run loop down. A later Broadcast must still be delivered.
func TestOnConnect_PanicDoesNotKillRunLoop(t *testing.T) {
	s := New(DefaultConfig())
	logger := &errCapLogger{}
	s.SetLogger(logger)
	s.OnConnect(func(c *Client) {
		panic("boom from OnConnect")
	})

	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer s.Shutdown(ctx)

	ts := httptest.NewServer(http.HandlerFunc(s.HandleConnection))
	defer ts.Close()

	ws := dialClient(t, ts.URL)
	defer ws.Close()

	// The panic is recovered and logged on the run-loop goroutine.
	testsync.Eventually(t, func() bool {
		return logger.has("connect listener panic recovered")
	}, 5*time.Second, "OnConnect panic recovered and logged")

	// Run loop must still be alive: a Broadcast is delivered to the client.
	s.Broadcast(Message{Type: "ping", Data: "after-panic"})

	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		var msg Message
		if err := ws.ReadJSON(&msg); err != nil {
			t.Fatalf("did not receive broadcast after OnConnect panic (run loop dead?): %v", err)
		}
		if msg.Type == "ping" {
			break
		}
	}
}

// TestStart_AfterShutdown_ReturnsErrServerClosed is B50: the lifecycle is
// one-shot. A Start after Shutdown returns ErrServerClosed.
func TestStart_AfterShutdown_ReturnsErrServerClosed(t *testing.T) {
	s := New(DefaultConfig())
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	err := s.Start()
	if !errors.Is(err, ErrServerClosed) {
		t.Fatalf("Start after Shutdown: want ErrServerClosed, got %v", err)
	}
}

// TestStart_AfterShutdownBeforeStart_ReturnsErrServerClosed guards the
// one-shot lifecycle when Shutdown runs before Start ever did: any Shutdown is
// terminal, so the subsequent Start must still return ErrServerClosed.
func TestStart_AfterShutdownBeforeStart_ReturnsErrServerClosed(t *testing.T) {
	s := New(DefaultConfig())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown before Start: %v", err)
	}

	err := s.Start()
	if !errors.Is(err, ErrServerClosed) {
		t.Fatalf("Start after Shutdown-before-Start: want ErrServerClosed, got %v", err)
	}
}

// TestBroadcast_AfterShutdown_DoesNotBlock is B50: once the run loop has exited,
// Broadcast must drop (via the stopChan select) instead of wedging forever on
// the undrained, buffer-capped channel.
func TestBroadcast_AfterShutdown_DoesNotBlock(t *testing.T) {
	s := New(DefaultConfig())
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	done := make(chan struct{})
	go func() { //safe-goroutine: bounded loop, exits or test fails the timeout
		// Far exceed the 256 buffer cap: a wedging Broadcast would block here.
		for i := 0; i < 1000; i++ {
			s.Broadcast(Message{Type: "x"})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Broadcast blocked after Shutdown (expected drop via stopChan select)")
	}
}

// TestHandleUnregister_ConcurrentGroupOps_NoRace is B51: handleUnregister reads
// client.Groups, which is guarded by client.mu. Concurrent JoinGroup/LeaveGroup
// against a disconnecting client must not race. Run under -race to exercise.
func TestHandleUnregister_ConcurrentGroupOps_NoRace(t *testing.T) {
	s := New(DefaultConfig())

	client := &Client{
		ID:       "c1",
		Send:     make(chan Message, 256),
		Server:   s,
		Groups:   make(map[string]bool),
		Metadata: make(map[string]interface{}),
	}
	s.mu.Lock()
	s.clients[client.ID] = client
	s.mu.Unlock()

	const groups = 50
	for i := 0; i < groups; i++ {
		_ = s.JoinGroup(client.ID, fmt.Sprintf("g%d", i))
	}

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = s.JoinGroup(client.ID, fmt.Sprintf("g%d", i%groups))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = s.LeaveGroup(client.ID, fmt.Sprintf("g%d", i%groups))
		}
	}()
	go func() {
		defer wg.Done()
		// Drive the unregister path concurrently with the group churn.
		s.handleUnregister(client)
	}()
	wg.Wait()
}
