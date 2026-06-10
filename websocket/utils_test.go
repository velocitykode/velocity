package websocket

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

// TestGenerateIDRandFailure verifies generateID fails closed when the
// randomness source errors instead of returning a zero (predictable) ID.
func TestGenerateIDRandFailure(t *testing.T) {
	orig := randRead
	defer func() { randRead = orig }()

	wantErr := errors.New("entropy exhausted")
	randRead = func(b []byte) (int, error) { return 0, wantErr }

	id, err := generateID()
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected rand error, got %v", err)
	}
	if id != "" {
		t.Errorf("expected empty ID on failure, got %q", id)
	}
}

// TestHandleConnectionRandFailure verifies a randomness failure refuses the
// upgrade (no zero-ID client is ever admitted) and that the server stays
// healthy afterwards: connection slots are released and a subsequent
// connection with working randomness succeeds.
func TestHandleConnectionRandFailure(t *testing.T) {
	orig := randRead
	defer func() { randRead = orig }()

	s := New(DefaultConfig())
	if err := s.Start(); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer s.Shutdown(context.Background())

	ts := httptest.NewServer(http.HandlerFunc(s.HandleConnection))
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	// Break the randomness source: the upgrade must be refused.
	randRead = func(b []byte) (int, error) { return 0, errors.New("entropy exhausted") }

	ws, resp, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
	if err == nil {
		ws.Close()
		t.Fatal("expected connection to be refused when randomness fails")
	}
	if resp == nil || resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected HTTP 500 refusal, got %+v", resp)
	}
	if got := s.GetStats().ConnectedClients; got != 0 {
		t.Errorf("expected 0 connected clients after refusal, got %d", got)
	}

	// Restore randomness: the slot reserved for the failed attempt must have
	// been released, so a fresh connection succeeds.
	randRead = orig

	ws, _, err = websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
	if err != nil {
		t.Fatalf("expected connection to succeed after randomness restored: %v", err)
	}
	defer ws.Close()

	var msg Message
	if err := ws.ReadJSON(&msg); err != nil {
		t.Fatalf("Failed to read welcome message: %v", err)
	}
	if msg.Type != "welcome" {
		t.Errorf("Expected welcome message, got %s", msg.Type)
	}
}
