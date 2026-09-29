package websocket

import (
	"context"
	"errors"
	"github.com/velocitykode/velocity/contract"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/hostile"
)

// A Shutdown called from one of the server's own goroutines (the run loop
// running a connect callback, a client's read pump running a message
// handler, the broadcast fan-out) cannot wait for the goroutines it runs
// on: it stops the server and returns an error wrapping ErrServerClosed at
// once, and a Shutdown from outside then waits for the drain.
func TestServer_ShutdownFromAServerGoroutineDoesNotWait(t *testing.T) {
	entries := []struct {
		name  string
		setup func(s *Server, stop func())
		kick  func(t *testing.T, s *Server, tsURL string)
	}{
		{"connect callback", func(s *Server, stop func()) {
			s.OnConnect(func(*Client) { stop() })
		}, func(t *testing.T, s *Server, tsURL string) {
			ws := dialClient(t, tsURL)
			t.Cleanup(func() { _ = ws.Close() })
		}},
		{"message handler", func(s *Server, stop func()) {
			s.On("stop", func(*Client, Message) error {
				stop()
				return nil
			})
		}, func(t *testing.T, s *Server, tsURL string) {
			ws := dialClient(t, tsURL)
			t.Cleanup(func() { _ = ws.Close() })
			if err := ws.WriteJSON(Message{Type: "stop"}); err != nil {
				t.Fatalf("write: %v", err)
			}
		}},
		{"broadcast fan-out", func(s *Server, stop func()) {
			var once atomic.Bool
			s.fanoutHook = func() {
				if once.CompareAndSwap(false, true) {
					stop()
				}
			}
		}, func(t *testing.T, s *Server, tsURL string) {
			s.Broadcast(Message{Type: "kick"})
		}},
	}
	for _, e := range entries {
		t.Run(e.name, func(t *testing.T) {
			s := New(DefaultConfig())
			var stopErr atomic.Pointer[error]
			returned := make(chan struct{})
			var once atomic.Bool
			e.setup(s, func() {
				if !once.CompareAndSwap(false, true) {
					return
				}
				err := s.Shutdown(context.Background())
				stopErr.Store(&err)
				close(returned)
			})
			if err := s.Start(); err != nil {
				t.Fatalf("Start: %v", err)
			}
			ts := httptest.NewServer(http.HandlerFunc(s.HandleConnection))
			t.Cleanup(ts.Close)

			e.kick(t, s, ts.URL)
			hostile.Within(t, hostile.Deadline, func() { <-returned })
			if p := stopErr.Load(); p == nil || !errors.Is(*p, ErrServerClosed) || !errors.Is(*p, contract.ErrStopFromOwnWork) {
				t.Fatalf("Shutdown from a server goroutine = %v, want an error wrapping ErrServerClosed and contract.ErrStopFromOwnWork", p)
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

// A Shutdown that overlaps one already draining waits for that drain or
// its own ctx: with the drain held open by a blocked message handler, it
// returns at its own deadline instead of nil.
func TestServer_OverlappingShutdownWaitsForTheDrain(t *testing.T) {
	s := New(DefaultConfig())
	entered := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	s.On("hold", func(*Client, Message) error {
		close(entered)
		<-release
		return nil
	})
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ts := httptest.NewServer(http.HandlerFunc(s.HandleConnection))
	t.Cleanup(ts.Close)
	ws := dialClient(t, ts.URL)
	t.Cleanup(func() { _ = ws.Close() })
	if err := ws.WriteJSON(Message{Type: "hold"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	hostile.Within(t, hostile.Deadline, func() { <-entered })

	first, cancelFirst := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelFirst()
	if err := s.Shutdown(first); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first Shutdown = %v, want its deadline while a handler holds the drain", err)
	}
	second, cancelSecond := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelSecond()
	if err := s.Shutdown(second); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("overlapping Shutdown = %v, want its own deadline, not nil before the drain ended", err)
	}
	close(release)
	hostile.Within(t, hostile.Deadline, func() {
		ctx, cancel := context.WithTimeout(context.Background(), hostile.Deadline)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown after the drain = %v, want nil", err)
		}
	})
}

// Broadcast called on the run loop (from a connect callback) cannot wait
// for the run loop to drain the broadcast channel: past the channel's
// buffer it delivers inline, so the callback returns and the server keeps
// serving.
func TestServer_BroadcastFromTheRunLoopDoesNotWaitForItself(t *testing.T) {
	s := New(DefaultConfig())
	returned := make(chan struct{})
	s.OnConnect(func(*Client) {
		for i := 0; i < 2*cap(s.broadcast); i++ {
			s.Broadcast(Message{Type: "burst"})
		}
		close(returned)
	})
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ts := httptest.NewServer(http.HandlerFunc(s.HandleConnection))
	t.Cleanup(ts.Close)
	ws := dialClient(t, ts.URL)
	t.Cleanup(func() { _ = ws.Close() })
	hostile.Within(t, hostile.Deadline, func() { <-returned })
	hostile.Within(t, hostile.Deadline, func() {
		ctx, cancel := context.WithTimeout(context.Background(), hostile.Deadline)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown = %v", err)
		}
	})
}
