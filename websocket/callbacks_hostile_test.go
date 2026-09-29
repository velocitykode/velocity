package websocket

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/hostile"
)

// The connect and disconnect callbacks and a message handler are user code
// on the server's own goroutines (the run loop, a client's read pump). One
// that panics is contained and the server keeps serving; one that blocks
// holds only its goroutine, so the server's other calls answer and a
// Shutdown returns at its deadline; one that calls back into the server
// (joining a group, broadcasting, even shutting it down) returns.
func TestServer_CallbacksAreContained(t *testing.T) {
	entries := []struct {
		name string
		arm  func(s *Server, run func(*Client))
		// kick reaches the callback with a connected client.
		kick func(t *testing.T, tsURL string)
	}{
		{"connect callback", func(s *Server, run func(*Client)) {
			s.OnConnect(func(c *Client) { run(c) })
		}, func(t *testing.T, tsURL string) {
			ws := dialClient(t, tsURL)
			t.Cleanup(func() { _ = ws.Close() })
		}},
		{"disconnect callback", func(s *Server, run func(*Client)) {
			s.OnDisconnect(func(c *Client) { run(c) })
		}, func(t *testing.T, tsURL string) {
			ws := dialClient(t, tsURL)
			_ = ws.Close()
		}},
		{"message handler", func(s *Server, run func(*Client)) {
			s.On("go", func(c *Client, _ Message) error {
				run(c)
				return nil
			})
		}, func(t *testing.T, tsURL string) {
			ws := dialClient(t, tsURL)
			t.Cleanup(func() { _ = ws.Close() })
			if err := ws.WriteJSON(Message{Type: "go"}); err != nil {
				t.Fatalf("write: %v", err)
			}
		}},
	}
	for _, mode := range hostile.Modes() {
		for _, e := range entries {
			t.Run(mode.String()+"/"+e.name, func(t *testing.T) {
				s := New(DefaultConfig())
				var client atomic.Pointer[Client]
				var nestedErr atomic.Pointer[error]
				code := hostile.New(t, mode, func() {
					c := client.Load()
					_ = s.JoinGroup(c.ID, "room")
					s.Broadcast(Message{Type: "again"})
					_ = s.GetClients()
					err := s.Shutdown(context.Background())
					nestedErr.Store(&err)
				})
				e.arm(s, func(c *Client) {
					client.Store(c)
					code.Run()
				})
				if err := s.Start(); err != nil {
					t.Fatalf("Start: %v", err)
				}
				ts := httptest.NewServer(http.HandlerFunc(s.HandleConnection))
				t.Cleanup(ts.Close)

				e.kick(t, ts.URL)
				hostile.Within(t, hostile.Deadline, func() { <-code.Entered() })

				switch mode {
				case hostile.Panic:
					// The server keeps serving: another client connects.
					hostile.Within(t, hostile.Deadline, func() {
						ws := dialClient(t, ts.URL)
						_ = ws.Close()
					})
				case hostile.Block:
					hostile.Within(t, hostile.Deadline, func() {
						_ = s.GetClients()
						s.Broadcast(Message{Type: "meanwhile"})
						ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
						defer cancel()
						if err := s.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
							t.Errorf("Shutdown while a callback blocks = %v, want its deadline", err)
						}
					})
					code.Release()
				case hostile.Reenter:
					waitFor(t, func() bool { return nestedErr.Load() != nil }, "the re-entering callback returning")
					if err := *nestedErr.Load(); !errors.Is(err, ErrServerClosed) {
						t.Errorf("Shutdown from the callback = %v, want an error wrapping ErrServerClosed", err)
					}
				}
				hostile.Within(t, hostile.Deadline, func() {
					ctx, cancel := context.WithTimeout(context.Background(), hostile.Deadline)
					defer cancel()
					if err := s.Shutdown(ctx); err != nil {
						t.Errorf("Shutdown from outside = %v, want nil once drained", err)
					}
				})
			})
		}
	}
}

// waitFor polls cond until it holds or hostile.Deadline passes.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(hostile.Deadline)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}
