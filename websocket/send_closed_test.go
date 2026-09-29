package websocket

import (
	"errors"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
)

// Sending to a client that disconnected reports it instead of panicking
// on its closed send channel.
func TestClient_SendToADisconnectedClient(t *testing.T) {
	s := New(Config{})
	c := addTestClient(s, "c1")
	if err := s.JoinGroup("c1", "room"); err != nil {
		t.Fatalf("premise: %v", err)
	}
	c.closeSend()
	sends := map[string]func() error{
		"SendMessage":         func() error { return c.SendMessage(Message{Type: "x"}) },
		"SendToClient":        func() error { return s.SendToClient("c1", Message{Type: "x"}) },
		"BroadcastToGroup":    func() error { return s.BroadcastToGroup("room", Message{Type: "x"}) },
		"SendToOthersInGroup": func() error { return s.SendToOthersInGroup("room", "other", Message{Type: "x"}) },
	}
	for name, send := range sends {
		t.Run(name, func(t *testing.T) {
			var err error
			if p := hostile.Within(t, hostile.Deadline, func() { err = send() }); p != nil {
				t.Fatalf("panicked: %v", p)
			}
			if (name == "SendMessage" || name == "SendToClient") && !errors.Is(err, ErrClientNotFound) {
				t.Fatalf("err = %v, want ErrClientNotFound", err)
			}
		})
	}
}
