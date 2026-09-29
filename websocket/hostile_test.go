package websocket

import (
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
)

// addTestClient registers a client directly, with no connection.
func addTestClient(s *Server, id string) *Client {
	c := &Client{ID: id, Send: make(chan Message, 4), Server: s, Groups: map[string]bool{}, Metadata: map[string]interface{}{}}
	s.mu.Lock()
	s.clients[id] = c
	s.mu.Unlock()
	return c
}

// JoinGroup and LeaveGroup log with the server lock released: a logger
// that panics, blocks or reads the server leaves every other server call
// working, and the membership change stands.
func TestServer_GroupLinesAreLoggedUnlocked(t *testing.T) {
	entries := []struct {
		name string
		call func(s *Server) error
		want bool
	}{
		{"JoinGroup", func(s *Server) error { return s.JoinGroup("c1", "room") }, true},
		{"LeaveGroup", func(s *Server) error { return s.LeaveGroup("c1", "room") }, false},
	}
	for _, mode := range hostile.Modes() {
		for _, e := range entries {
			t.Run(mode.String()+"/"+e.name, func(t *testing.T) {
				s := New(Config{})
				addTestClient(s, "c1")
				if e.name == "LeaveGroup" {
					if err := s.JoinGroup("c1", "room"); err != nil {
						t.Fatalf("premise: %v", err)
					}
				}
				code := hostile.New(t, mode, func() {
					_ = s.GetGroupMembers("room")
					_ = s.GetClients()
				})
				s.SetLogger(hostile.NewLogger(code, hostile.Info))

				call := func() { _ = e.call(s) }
				if mode == hostile.Block {
					go func() { //safe-goroutine: the test releases the block below
						_ = e.call(s)
					}()
					<-code.Entered()
					call = func() {
						_ = s.GetGroupMembers("room")
						_ = s.GetClients()
					}
				}
				if p := hostile.Within(t, hostile.Deadline, call); p != nil && mode != hostile.Panic {
					t.Fatalf("panicked: %v", p)
				}
				code.Release()
				code.Disarm()
				hostile.Within(t, hostile.Deadline, func() {
					inGroup := len(s.GetGroupMembers("room")) == 1
					if inGroup != e.want {
						t.Errorf("client in group = %v after %s, want %v", inGroup, e.name, e.want)
					}
				})
			})
		}
	}
}

// On builds a handler's middleware chain with the server lock released:
// a middleware that panics, blocks or calls back into the server leaves
// the server working, and a retry registers the handler.
func TestServer_OnBuildsTheMiddlewareChainUnlocked(t *testing.T) {
	for _, mode := range hostile.Modes() {
		t.Run(mode.String(), func(t *testing.T) {
			s := New(Config{})
			code := hostile.New(t, mode, func() {
				s.Use(func(next MessageHandler) MessageHandler { return next })
				_ = s.GetClients()
			})
			s.Use(func(next MessageHandler) MessageHandler {
				code.Run()
				return next
			})
			handler := func(*Client, Message) error { return nil }
			call := func() { s.On("chat", handler) }
			if mode == hostile.Block {
				go func() { //safe-goroutine: the test releases the block below
					s.On("chat", handler)
				}()
				<-code.Entered()
				call = func() { _ = s.GetClients() }
			}
			if p := hostile.Within(t, hostile.Deadline, call); p != nil && mode != hostile.Panic {
				t.Fatalf("panicked: %v", p)
			}
			code.Release()
			code.Disarm()
			hostile.Within(t, hostile.Deadline, func() {
				s.On("chat", handler)
				s.mu.RLock()
				_, ok := s.handlers["chat"]
				s.mu.RUnlock()
				if !ok {
					t.Error("the handler is not registered after the retry")
				}
			})
		})
	}
}

// The server logs from inside its own recovers: a logger that panics
// there is contained, so the recover still ends normally and the run loop
// or fanout goroutine that called it lives on.
func TestServer_RecoverLinesContainAPanickingLogger(t *testing.T) {
	s := New(Config{})
	code := hostile.New(t, hostile.Panic, nil)
	s.SetLogger(hostile.NewLogger(code))
	if p := hostile.Within(t, hostile.Deadline, func() {
		s.callWithRecover("test", func() { panic("handler broke") })
	}); p != nil {
		t.Fatalf("callWithRecover let the logger's panic out: %v", p)
	}
	if code.Calls() == 0 {
		t.Fatal("premise: the recover logged nothing")
	}
	if s.RecoveredPanics() != 1 {
		t.Fatalf("recovered panics = %d, want 1", s.RecoveredPanics())
	}
}
