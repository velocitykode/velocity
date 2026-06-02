package websocket

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	testsync "github.com/velocitykode/velocity/testing"
)

func TestNewServer(t *testing.T) {
	config := DefaultConfig()
	s := New(config)

	if s == nil {
		t.Fatal("Expected server to be created")
	}

	if s.config.Host != "0.0.0.0" {
		t.Errorf("Expected host to be 0.0.0.0, got %s", s.config.Host)
	}

	if s.config.Port != 6001 {
		t.Errorf("Expected port to be 6001, got %d", s.config.Port)
	}

	if s.config.Path != "/ws" {
		t.Errorf("Expected path to be /ws, got %s", s.config.Path)
	}
}

func TestServerStartStop(t *testing.T) {
	s := New(DefaultConfig())

	err := s.Start()
	if err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}

	if !s.running {
		t.Error("Expected server to be running")
	}

	// Try starting again (should fail)
	err = s.Start()
	if err == nil {
		t.Error("Expected error when starting already running server")
	}

	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}

	if s.running {
		t.Error("Expected server to be stopped")
	}
}

func TestHandleConnection(t *testing.T) {
	s := New(DefaultConfig())
	s.Start()
	defer s.Shutdown(context.Background())

	// Create test server
	ts := httptest.NewServer(http.HandlerFunc(s.HandleConnection))
	defer ts.Close()

	// Connect to WebSocket
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
	if err != nil {
		t.Fatalf("Failed to connect to WebSocket: %v", err)
	}
	defer ws.Close()

	// Wait for welcome message
	var msg Message
	err = ws.ReadJSON(&msg)
	if err != nil {
		t.Fatalf("Failed to read welcome message: %v", err)
	}

	if msg.Type != "welcome" {
		t.Errorf("Expected welcome message, got %s", msg.Type)
	}

	// Check stats
	stats := s.GetStats()
	if stats.ConnectedClients != 1 {
		t.Errorf("Expected 1 connected client, got %d", stats.ConnectedClients)
	}
}

func TestMessageHandler(t *testing.T) {
	s := New(DefaultConfig())
	s.Start()
	defer s.Shutdown(context.Background())

	// Register echo handler
	s.On("echo", func(client *Client, msg Message) error {
		return client.SendMessage(Message{
			Type: "echo_response",
			Data: msg.Data,
		})
	})

	// Create test server
	ts := httptest.NewServer(http.HandlerFunc(s.HandleConnection))
	defer ts.Close()

	// Connect
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer ws.Close()

	// Skip welcome message
	var welcome Message
	ws.ReadJSON(&welcome)

	// Send echo message
	err = ws.WriteJSON(Message{
		Type: "echo",
		Data: "hello",
	})
	if err != nil {
		t.Fatalf("Failed to send message: %v", err)
	}

	// Read response
	var response Message
	err = ws.ReadJSON(&response)
	if err != nil {
		t.Fatalf("Failed to read response: %v", err)
	}

	if response.Type != "echo_response" {
		t.Errorf("Expected echo_response, got %s", response.Type)
	}

	if response.Data != "hello" {
		t.Errorf("Expected data to be hello, got %v", response.Data)
	}
}

func TestGroups(t *testing.T) {
	s := New(DefaultConfig())
	s.Start()
	defer s.Shutdown(context.Background())

	// Create test clients
	ts := httptest.NewServer(http.HandlerFunc(s.HandleConnection))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	// Connect client 1
	ws1, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
	if err != nil {
		t.Fatalf("Failed to connect client 1: %v", err)
	}
	defer ws1.Close()

	// Connect client 2
	ws2, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
	if err != nil {
		t.Fatalf("Failed to connect client 2: %v", err)
	}
	defer ws2.Close()

	// Poll until both clients are registered on the server side
	testsync.Eventually(t, func() bool { return s.GetStats().ConnectedClients == 2 }, 2*time.Second, "both clients registered")

	// Get client IDs from welcome messages
	var welcome1, welcome2 Message
	ws1.ReadJSON(&welcome1)
	ws2.ReadJSON(&welcome2)

	client1ID := welcome1.Data.(map[string]interface{})["id"].(string)
	client2ID := welcome2.Data.(map[string]interface{})["id"].(string)

	// Join both clients to a group
	err = s.JoinGroup(client1ID, "test-group")
	if err != nil {
		t.Fatalf("Failed to join client 1 to group: %v", err)
	}

	err = s.JoinGroup(client2ID, "test-group")
	if err != nil {
		t.Fatalf("Failed to join client 2 to group: %v", err)
	}

	// Check group members
	members := s.GetGroupMemberIDs("test-group")
	if len(members) != 2 {
		t.Errorf("Expected 2 members in group, got %d", len(members))
	}

	// Broadcast to group
	err = s.BroadcastToGroup("test-group", Message{
		Type: "group_message",
		Data: "hello group",
	})
	if err != nil {
		t.Fatalf("Failed to broadcast to group: %v", err)
	}

	// Both clients should receive the message
	var msg1, msg2 Message
	err = ws1.ReadJSON(&msg1)
	if err != nil {
		t.Fatalf("Client 1 failed to receive group message: %v", err)
	}

	err = ws2.ReadJSON(&msg2)
	if err != nil {
		t.Fatalf("Client 2 failed to receive group message: %v", err)
	}

	if msg1.Type != "group_message" || msg2.Type != "group_message" {
		t.Error("Expected both clients to receive group_message")
	}

	// Leave group
	err = s.LeaveGroup(client1ID, "test-group")
	if err != nil {
		t.Fatalf("Failed to leave group: %v", err)
	}

	members = s.GetGroupMemberIDs("test-group")
	if len(members) != 1 {
		t.Errorf("Expected 1 member in group after leave, got %d", len(members))
	}
}

func TestBroadcast(t *testing.T) {
	s := New(DefaultConfig())
	s.Start()
	defer s.Shutdown(context.Background())

	ts := httptest.NewServer(http.HandlerFunc(s.HandleConnection))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	// Connect multiple clients
	clients := make([]*websocket.Conn, 3)
	for i := range clients {
		ws, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
		if err != nil {
			t.Fatalf("Failed to connect client %d: %v", i, err)
		}
		defer ws.Close()
		clients[i] = ws

		// Skip welcome message
		var welcome Message
		ws.ReadJSON(&welcome)
	}

	// Poll until all 3 clients are registered
	testsync.Eventually(t, func() bool { return s.GetStats().ConnectedClients == 3 }, 2*time.Second, "all clients registered")

	// Broadcast message
	s.Broadcast(Message{
		Type: "broadcast",
		Data: "hello everyone",
	})

	// All clients should receive the message
	for i, ws := range clients {
		var msg Message
		err := ws.ReadJSON(&msg)
		if err != nil {
			t.Fatalf("Client %d failed to receive broadcast: %v", i, err)
		}

		if msg.Type != "broadcast" {
			t.Errorf("Client %d expected broadcast message, got %s", i, msg.Type)
		}

		if msg.Data != "hello everyone" {
			t.Errorf("Client %d expected data 'hello everyone', got %v", i, msg.Data)
		}
	}
}

func TestHandleRaw(t *testing.T) {
	s := New(DefaultConfig())

	// Create test server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := s.HandleRaw(w, r)
		if err != nil {
			t.Errorf("HandleRaw failed: %v", err)
			return
		}
		defer conn.Close()

		// Echo raw messages back
		for {
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(mt, msg); err != nil {
				return
			}
		}
	}))
	defer ts.Close()

	// Connect to WebSocket
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
	if err != nil {
		t.Fatalf("Failed to connect to WebSocket: %v", err)
	}
	defer ws.Close()

	// Send a text message
	err = ws.WriteMessage(websocket.TextMessage, []byte("hello raw"))
	if err != nil {
		t.Fatalf("Failed to send message: %v", err)
	}

	// Read echoed response
	mt, msg, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("Failed to read message: %v", err)
	}

	if mt != websocket.TextMessage {
		t.Errorf("Expected text message type, got %d", mt)
	}
	if string(msg) != "hello raw" {
		t.Errorf("Expected 'hello raw', got '%s'", string(msg))
	}

	// Send a binary message
	binaryData := []byte{0x00, 0x01, 0x02, 0x03}
	err = ws.WriteMessage(websocket.BinaryMessage, binaryData)
	if err != nil {
		t.Fatalf("Failed to send binary message: %v", err)
	}

	mt, msg, err = ws.ReadMessage()
	if err != nil {
		t.Fatalf("Failed to read binary message: %v", err)
	}

	if mt != websocket.BinaryMessage {
		t.Errorf("Expected binary message type, got %d", mt)
	}
	if string(msg) != string(binaryData) {
		t.Errorf("Binary data mismatch")
	}
}

func TestHandleRaw_DoesNotRegisterClient(t *testing.T) {
	s := New(DefaultConfig())
	s.Start()
	defer s.Shutdown(context.Background())

	// Create test server using HandleRaw
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := s.HandleRaw(w, r)
		if err != nil {
			t.Errorf("HandleRaw failed: %v", err)
			return
		}
		defer conn.Close()

		// Keep connection open briefly
		time.Sleep(200 * time.Millisecond)
	}))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer ws.Close()

	// Raw connections bypass client management entirely — HandleRaw never
	// adds to s.clients or bumps ConnectedClients — so this assertion
	// holds regardless of timing. No sleep needed.
	stats := s.GetStats()
	if stats.ConnectedClients != 0 {
		t.Errorf("Expected 0 connected clients for raw connection, got %d", stats.ConnectedClients)
	}

	clients := s.GetClients()
	if len(clients) != 0 {
		t.Errorf("Expected 0 managed clients for raw connection, got %d", len(clients))
	}
}

func TestHandleRaw_RespectsOriginCheck(t *testing.T) {
	config := DefaultConfig()
	config.AllowedOrigins = []string{"https://allowed.example.com"}
	s := New(config)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := s.HandleRaw(w, r)
		if err != nil {
			return
		}
		conn.Close()
	}))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	// Connection with disallowed origin should fail
	header := http.Header{}
	header.Set("Origin", "https://evil.example.com")
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err == nil {
		t.Fatal("Expected connection with disallowed origin to fail")
	}
	if resp != nil && resp.StatusCode != http.StatusForbidden {
		t.Errorf("Expected status 403, got %d", resp.StatusCode)
	}
}

func TestConnectionLimit(t *testing.T) {
	config := DefaultConfig()
	config.MaxConnections = 2
	s := New(config)
	s.Start()
	defer s.Shutdown(context.Background())

	ts := httptest.NewServer(http.HandlerFunc(s.HandleConnection))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	// Connect first client (should succeed)
	ws1, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
	if err != nil {
		t.Fatalf("Failed to connect client 1: %v", err)
	}
	defer ws1.Close()

	// Connect second client (should succeed)
	ws2, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
	if err != nil {
		t.Fatalf("Failed to connect client 2: %v", err)
	}
	defer ws2.Close()

	// Poll until both clients are registered so the limit check is meaningful
	testsync.Eventually(t, func() bool { return s.GetStats().ConnectedClients == 2 }, 2*time.Second, "two clients registered before testing limit")

	// Connect third client (should fail due to limit)
	ws3, resp, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
	if err == nil {
		ws3.Close()
		t.Fatal("Expected connection to fail due to limit")
	}

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("Expected status 503, got %d", resp.StatusCode)
	}
}

func TestConnectionLimit_Concurrent(t *testing.T) {
	config := DefaultConfig()
	config.MaxConnections = 5
	s := New(config)
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Shutdown(context.Background())

	ts := httptest.NewServer(http.HandlerFunc(s.HandleConnection))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	const attempts = 30

	start := make(chan struct{})
	done := make(chan struct{})
	var wg sync.WaitGroup
	var acceptedMu sync.Mutex
	accepted := make([]*websocket.Conn, 0, config.MaxConnections)
	var rejected atomic.Int64
	var unexpected atomic.Int64
	var maxObserved atomic.Int64

	go func() {
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				observed := s.GetStats().ConnectedClients
				for {
					current := maxObserved.Load()
					if observed <= current || maxObserved.CompareAndSwap(current, observed) {
						break
					}
				}
			case <-done:
				return
			}
		}
	}()

	wg.Add(attempts)
	for i := 0; i < attempts; i++ {
		go func() {
			defer wg.Done()
			<-start

			ws, resp, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
			if err == nil {
				acceptedMu.Lock()
				accepted = append(accepted, ws)
				acceptedMu.Unlock()
				return
			}
			if resp != nil && resp.StatusCode == http.StatusServiceUnavailable {
				rejected.Add(1)
				return
			}
			unexpected.Add(1)
		}()
	}

	close(start)
	wg.Wait()
	close(done)

	acceptedMu.Lock()
	acceptedCount := len(accepted)
	for _, ws := range accepted {
		defer ws.Close()
	}
	acceptedMu.Unlock()

	if unexpected.Load() != 0 {
		t.Fatalf("unexpected dial failures: %d", unexpected.Load())
	}
	if acceptedCount > config.MaxConnections {
		t.Fatalf("accepted %d connections, want at most %d", acceptedCount, config.MaxConnections)
	}
	if rejected.Load() != attempts-int64(acceptedCount) {
		t.Fatalf("rejected %d connections, want %d", rejected.Load(), attempts-acceptedCount)
	}

	testsync.Eventually(t, func() bool {
		return s.GetStats().ConnectedClients == int64(acceptedCount)
	}, 2*time.Second, "accepted connections registered")

	if got := s.GetStats().ConnectedClients; got > int64(config.MaxConnections) {
		t.Fatalf("ConnectedClients = %d, want at most %d", got, config.MaxConnections)
	}
	if got := maxObserved.Load(); got > int64(config.MaxConnections) {
		t.Fatalf("observed ConnectedClients peak %d, want at most %d", got, config.MaxConnections)
	}
}

func TestConnectionLimit_DecrementsOnDisconnect(t *testing.T) {
	config := DefaultConfig()
	config.MaxConnections = 3
	s := New(config)
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Shutdown(context.Background())

	ts := httptest.NewServer(http.HandlerFunc(s.HandleConnection))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	clients := make([]*websocket.Conn, 0, config.MaxConnections)
	for i := 0; i < config.MaxConnections; i++ {
		ws, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
		if err != nil {
			t.Fatalf("dial client %d: %v", i, err)
		}
		clients = append(clients, ws)
		defer ws.Close()
	}

	testsync.Eventually(t, func() bool {
		return s.GetStats().ConnectedClients == int64(config.MaxConnections)
	}, 2*time.Second, "connections reached cap")

	clients[0].Close()
	clients[1].Close()

	testsync.Eventually(t, func() bool {
		return s.GetStats().ConnectedClients == int64(config.MaxConnections-2)
	}, 2*time.Second, "closed connections released slots")

	for i := 0; i < 2; i++ {
		ws, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
		if err != nil {
			t.Fatalf("dial replacement %d: %v", i, err)
		}
		defer ws.Close()
	}

	testsync.Eventually(t, func() bool {
		return s.GetStats().ConnectedClients == int64(config.MaxConnections)
	}, 2*time.Second, "replacement connections admitted")
}

func TestServer_Callbacks_ConcurrentSetAndConnect(t *testing.T) {
	s := New(DefaultConfig())
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Shutdown(context.Background())

	ts := httptest.NewServer(http.HandlerFunc(s.HandleConnection))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	done := make(chan struct{})
	var setters sync.WaitGroup
	setters.Add(1)
	go func() {
		defer setters.Done()
		for {
			select {
			case <-done:
				return
			default:
			}

			s.OnConnect(func(*Client) {})
			s.OnDisconnect(func(*Client) {})
			s.OnError(func(*Client, error) {})
			s.OnConnect(nil)
			s.OnDisconnect(nil)
			s.OnError(nil)
		}
	}()

	for i := 0; i < 25; i++ {
		ws, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
		if err != nil {
			close(done)
			setters.Wait()
			t.Fatalf("Dial %d: %v", i, err)
		}

		var welcome Message
		if err := ws.ReadJSON(&welcome); err != nil {
			ws.Close()
			close(done)
			setters.Wait()
			t.Fatalf("Read welcome %d: %v", i, err)
		}
		ws.Close()
	}

	close(done)
	setters.Wait()
	testsync.Eventually(t, func() bool { return s.GetStats().ConnectedClients == 0 }, 2*time.Second, "clients disconnected")
}

func TestServer_OnErrorCallbackAndGenericFallback(t *testing.T) {
	s := New(DefaultConfig())
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Shutdown(context.Background())

	s.On("fail", func(*Client, Message) error {
		return errors.New("sensitive handler failure")
	})

	ts := httptest.NewServer(http.HandlerFunc(s.HandleConnection))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	ws, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
	if err != nil {
		t.Fatalf("Dial generic fallback client: %v", err)
	}
	var welcome Message
	if err := ws.ReadJSON(&welcome); err != nil {
		ws.Close()
		t.Fatalf("Read welcome: %v", err)
	}
	if err := ws.WriteJSON(Message{Type: "fail"}); err != nil {
		ws.Close()
		t.Fatalf("Write fail message: %v", err)
	}
	var response Message
	if err := ws.ReadJSON(&response); err != nil {
		ws.Close()
		t.Fatalf("Read generic error: %v", err)
	}
	ws.Close()
	if response.Type != "error" {
		t.Fatalf("Expected generic error response, got %s", response.Type)
	}
	data, ok := response.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("Expected response data map, got %T", response.Data)
	}
	if data["message"] != "internal error" {
		t.Fatalf("Expected generic internal error message, got %v", data["message"])
	}

	var errorCalls atomic.Int64
	errorSeen := make(chan struct{}, 1)
	s.OnError(func(*Client, error) {
		errorCalls.Add(1)
		select {
		case errorSeen <- struct{}{}:
		default:
		}
	})

	wsWithCallback, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
	if err != nil {
		t.Fatalf("Dial callback client: %v", err)
	}
	defer wsWithCallback.Close()
	if err := wsWithCallback.ReadJSON(&welcome); err != nil {
		t.Fatalf("Read callback welcome: %v", err)
	}
	if err := wsWithCallback.WriteJSON(Message{Type: "fail"}); err != nil {
		t.Fatalf("Write callback fail message: %v", err)
	}

	select {
	case <-errorSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("Expected OnError callback to run")
	}
	if got := errorCalls.Load(); got != 1 {
		t.Fatalf("Expected 1 OnError call, got %d", got)
	}
}

// TestServer_ShutdownWaitsForRunGoroutine verifies that Shutdown drains the
// run-loop goroutine before returning and leaves no goroutine behind.
func TestServer_ShutdownWaitsForRunGoroutine(t *testing.T) {
	s := New(DefaultConfig())
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Baseline after Start so runtime/test infra goroutines are already
	// accounted for; only the run loop should still be outstanding.
	baseline := runtime.NumGoroutine()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}

	// After Shutdown the run goroutine must be gone. Poll briefly because
	// the scheduler may not have parked the returning goroutine yet.
	testsync.Eventually(t, func() bool {
		return runtime.NumGoroutine() <= baseline-1
	}, 2*time.Second, "run goroutine exited")

	// Second call is a no-op and must return nil.
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown returned error: %v", err)
	}
}

// TestServer_ShutdownWaitsForClientPumps verifies that Shutdown waits for
// every per-client read/write pump to exit before returning.
func TestServer_ShutdownWaitsForClientPumps(t *testing.T) {
	s := New(DefaultConfig())
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ts := httptest.NewServer(http.HandlerFunc(s.HandleConnection))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	// Two long-lived clients, each with a read/write pump on the server.
	ws1, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
	if err != nil {
		t.Fatalf("dial 1: %v", err)
	}
	defer ws1.Close()
	ws2, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
	if err != nil {
		t.Fatalf("dial 2: %v", err)
	}
	defer ws2.Close()

	testsync.Eventually(t, func() bool { return s.GetStats().ConnectedClients == 2 },
		2*time.Second, "both clients registered")

	baseline := runtime.NumGoroutine()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}

	// run + 2 read + 2 write = 5 tracked goroutines must have exited.
	testsync.Eventually(t, func() bool {
		return runtime.NumGoroutine() <= baseline-5
	}, 2*time.Second, "all tracked goroutines exited")
}

// TestServer_ShutdownRespectsCtxDeadline verifies that Shutdown honours the
// caller's deadline when a tracked goroutine refuses to exit. We register a
// synthetic goroutine on the server WaitGroup that blocks until the test
// releases it, then assert Shutdown returns context.DeadlineExceeded rather
// than hanging.
func TestServer_ShutdownRespectsCtxDeadline(t *testing.T) {
	s := New(DefaultConfig())
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Simulate an uncooperative tracked goroutine. Because server_test.go
	// lives in package websocket, we can register directly on s.wg.
	release := make(chan struct{})
	var released sync.WaitGroup
	released.Add(1)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer released.Done()
		<-release
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := s.Shutdown(ctx)
	elapsed := time.Since(start)

	if err != context.DeadlineExceeded {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Shutdown hung for %s; should have returned near the 50ms deadline", elapsed)
	}

	// Release the stuck goroutine so the test exits cleanly.
	close(release)
	released.Wait()
}

// TestCheckOrigin_SameOriginDefault exercises the same-origin gate that runs
// whenever AllowedOrigins is empty. Three properties matter:
//
//  1. An empty Origin header is rejected by default (browsers always send
//     Origin on WS upgrades, so a missing one signals a non-browser caller
//     and must not be silently trusted).
//  2. A case-mismatched Origin host is accepted (Host headers and Origin
//     values often differ in case for IDN/punycode and ALL-CAPS environments).
//  3. A mismatched host is rejected.
//
// The opt-in AllowEmptyOrigin flag flips behaviour 1 back to accept.
func TestCheckOrigin_SameOriginDefault(t *testing.T) {
	tests := []struct {
		name             string
		origin           string
		host             string
		allowEmptyOrigin bool
		want             bool
	}{
		{
			name:   "missing Origin header is rejected by default",
			origin: "",
			host:   "example.com",
			want:   false,
		},
		{
			name:             "missing Origin header is accepted when opted in",
			origin:           "",
			host:             "example.com",
			allowEmptyOrigin: true,
			want:             true,
		},
		{
			name:   "case-mismatched Origin host accepted",
			origin: "https://Example.COM",
			host:   "example.com",
			want:   true,
		},
		{
			name:   "matching origin accepted",
			origin: "https://example.com",
			host:   "example.com",
			want:   true,
		},
		{
			name:   "mismatched host rejected",
			origin: "https://attacker.example.net",
			host:   "example.com",
			want:   false,
		},
		{
			name:   "malformed Origin rejected",
			origin: "::::not-a-url",
			host:   "example.com",
			want:   false,
		},
		// F2: Origin scheme allowlist. Browser WS upgrades only ever send
		// http(s) Origin values; anything else is a non-browser caller
		// trying to impersonate the host. The gate must reject them.
		{
			name:   "chrome-extension scheme rejected even with matching host",
			origin: "chrome-extension://example.com",
			host:   "example.com",
			want:   false,
		},
		{
			name:   "ftp scheme rejected even with matching host",
			origin: "ftp://example.com",
			host:   "example.com",
			want:   false,
		},
		{
			name:   "file scheme rejected",
			origin: "file://example.com",
			host:   "example.com",
			want:   false,
		},
		{
			name:   "ws scheme rejected (browsers do not send it as Origin)",
			origin: "ws://example.com",
			host:   "example.com",
			want:   false,
		},
		{
			name:   "http scheme with matching host accepted",
			origin: "http://example.com",
			host:   "example.com",
			want:   true,
		},
		{
			name:   "uppercase HTTPS scheme accepted (case-insensitive)",
			origin: "HTTPS://example.com",
			host:   "example.com",
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := DefaultConfig()
			config.AllowedOrigins = nil // same-origin only
			config.AllowEmptyOrigin = tt.allowEmptyOrigin
			s := New(config)

			req := httptest.NewRequest(http.MethodGet, "http://"+tt.host+"/ws", nil)
			req.Host = tt.host
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}

			got := s.checkOrigin(req)
			if got != tt.want {
				t.Errorf("checkOrigin(origin=%q host=%q allowEmpty=%v) = %v, want %v", tt.origin, tt.host, tt.allowEmptyOrigin, got, tt.want)
			}
		})
	}
}
