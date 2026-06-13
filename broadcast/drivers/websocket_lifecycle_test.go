package drivers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/velocitykode/velocity/broadcast"
	"github.com/velocitykode/velocity/websocket"
)

// TestBroadcastManager_Leave_RemovesSubscriber verifies Leave routes through
// the driver's Unsubscriber capability and actually drops the socket: the
// channel's GetClients snapshot shrinks. The count is taken from the opaque
// GetClients identifiers (not raw socket IDs), while Leave is called with the
// raw socket ID, exercising the identifier mapping the finding flagged.
func TestBroadcastManager_Leave_RemovesSubscriber(t *testing.T) {
	driver := NewWebSocketDriver(websocket.DefaultConfig())
	t.Cleanup(func() { _ = driver.Shutdown(context.Background()) })

	const channel = "presence-room.1"
	c1 := createTestClient("socket-1")
	c2 := createTestClient("socket-2")
	if err := driver.Subscribe(channel, c1); err != nil {
		t.Fatalf("Subscribe c1: %v", err)
	}
	if err := driver.Subscribe(channel, c2); err != nil {
		t.Fatalf("Subscribe c2: %v", err)
	}

	if got := len(driver.GetClients(channel)); got != 2 {
		t.Fatalf("GetClients before Leave = %d, want 2", got)
	}

	b := broadcast.New(driver)
	if err := b.Leave(channel, "socket-1"); err != nil {
		t.Fatalf("Leave: %v", err)
	}

	got := driver.GetClients(channel)
	if len(got) != 1 {
		t.Fatalf("GetClients after Leave = %d, want 1", len(got))
	}
}

// TestBroadcastManager_Shutdown_StopsServer verifies Shutdown reaches the
// websocket server through the Shutdowner capability: it returns within a
// bounded ctx and the now-stopped server rejects new connections with 503.
func TestBroadcastManager_Shutdown_StopsServer(t *testing.T) {
	driver := NewWebSocketDriver(websocket.DefaultConfig())
	b := broadcast.New(driver)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	driver.GetServer().HandleConnection(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("HandleConnection after Shutdown = %d, want 503", rec.Code)
	}
}

// TestWebSocketDriver_StartErr_Surfaced verifies a driver carrying a non-nil
// startErr reports it from the wire paths (BroadcastCtx / Subscribe) rather
// than silently doing nothing. The bare struct literal sidesteps the
// constructor, which always starts a fresh server successfully.
func TestWebSocketDriver_StartErr_Surfaced(t *testing.T) {
	sentinel := errors.New("boom: server already closed")
	d := &WebSocketDriver{
		channels:   make(map[string]map[string]*websocket.Client),
		clientSubs: make(map[string]map[string]struct{}),
		startErr:   sentinel,
	}

	if err := d.BroadcastCtx(context.Background(), []string{"any"}, "evt", "data"); !errors.Is(err, sentinel) {
		t.Errorf("BroadcastCtx = %v, want startErr", err)
	}
	if err := d.BroadcastExceptCtx(context.Background(), []string{"any"}, "evt", "data", "x"); !errors.Is(err, sentinel) {
		t.Errorf("BroadcastExceptCtx = %v, want startErr", err)
	}
	if err := d.Subscribe("any", createTestClient("c1")); !errors.Is(err, sentinel) {
		t.Errorf("Subscribe = %v, want startErr", err)
	}
	if err := d.Unsubscribe("any", "c1"); !errors.Is(err, sentinel) {
		t.Errorf("Unsubscribe = %v, want startErr", err)
	}
}
