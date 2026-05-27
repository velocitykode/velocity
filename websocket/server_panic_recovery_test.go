package websocket

import (
	"context"
	"testing"
	"time"

	testsync "github.com/velocitykode/velocity/testing"
)

// TestServer_RunLoopPanicRecovered covers audit D-04: a panic from
// handleRegister/Unregister/Broadcast must be caught by the inner
// callWithRecover so the consumer goroutine keeps draining the channels
// instead of exiting on the first failure.
//
// The test triggers a `send on closed channel` panic inside handleBroadcast
// by inserting a synthetic client whose Send channel has been closed. After
// the panic, RecoveredPanics() reflects the catch and the loop is still
// alive (verified separately by the follow-up loop-continues test).
func TestServer_RunLoopPanicRecovered(t *testing.T) {
	s := New(DefaultConfig())
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})

	// Synthetic client with a CLOSED Send channel. handleBroadcast will
	// hit `send on closed channel` when it iterates clients.
	bait := &Client{
		ID:   "panic-bait",
		Send: make(chan Message, 1),
	}
	close(bait.Send)

	s.mu.Lock()
	s.clients[bait.ID] = bait
	s.mu.Unlock()

	// Trigger the panic inside handleBroadcast.
	s.Broadcast(Message{Type: "boom", Data: "trigger"})

	// If the inner recover is missing, the run goroutine exits after the
	// first panic and RecoveredPanics stays at 0. With the recover, the
	// counter increments.
	testsync.Eventually(t, func() bool { return s.RecoveredPanics() >= 1 }, 2*time.Second, "broadcast panic recovered inside run loop")

	// Remove the synthetic client before Shutdown so the shutdown
	// connection-close walk does not deref the nil Conn pointer.
	s.mu.Lock()
	delete(s.clients, bait.ID)
	s.mu.Unlock()
}

// TestServer_RegisterPanicRecovered pins that a panic raised during
// handleRegister (here via a closed Send channel when handleRegister tries
// to enqueue the welcome message) is caught by callWithRecover so the loop
// keeps processing subsequent registers.
func TestServer_RegisterPanicRecovered(t *testing.T) {
	s := New(DefaultConfig())
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})

	// Closed Send channel so handleRegister panics on the welcome enqueue.
	bad := &Client{
		ID:   "bad-register",
		Send: make(chan Message),
	}
	close(bad.Send)

	s.register <- bad

	testsync.Eventually(t, func() bool { return s.RecoveredPanics() >= 1 }, 2*time.Second, "register panic recovered inside run loop")

	// Drop the synthetic client before Shutdown so the connection-close
	// walk does not deref the nil Conn pointer.
	s.mu.Lock()
	delete(s.clients, bad.ID)
	s.mu.Unlock()
}

// TestServer_RunLoopContinuesAfterPanic covers the core D-04 follow-up:
// after a handler panics, the consumer goroutine must keep draining the
// channels rather than exiting. The test fires multiple panicking
// broadcasts back to back and then a clean broadcast to a live client; if
// the run loop had died after the first panic, the live client would never
// receive the post-panic message.
func TestServer_RunLoopContinuesAfterPanic(t *testing.T) {
	s := New(DefaultConfig())
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})

	// Bait client: closed Send, forces handleBroadcast to panic on send.
	bait := &Client{
		ID:   "bait",
		Send: make(chan Message, 1),
	}
	close(bait.Send)

	// Live client: open Send buffer, used to verify subsequent broadcasts
	// still land after multiple panics.
	live := &Client{
		ID:   "live",
		Send: make(chan Message, 8),
	}

	s.mu.Lock()
	s.clients[bait.ID] = bait
	s.clients[live.ID] = live
	s.mu.Unlock()

	const panicBroadcasts = 3
	for i := 0; i < panicBroadcasts; i++ {
		s.Broadcast(Message{Type: "boom", Data: i})
	}

	// Wait until all expected panics have been recovered. If the run loop
	// exits after the first panic, this assertion fails (counter stuck at 1).
	testsync.Eventually(t, func() bool { return s.RecoveredPanics() >= panicBroadcasts }, 2*time.Second, "all panic broadcasts recovered")

	// Drain whatever the live client already received from the panicking
	// broadcasts (each panicking broadcast may or may not have delivered to
	// the live client first depending on map iteration order; that is fine,
	// we just need a clean slate for the post-panic assertion).
	for {
		select {
		case <-live.Send:
			continue
		default:
		}
		break
	}

	// Remove the bait client so the next broadcast does not panic again.
	s.mu.Lock()
	delete(s.clients, bait.ID)
	s.mu.Unlock()

	// Fire a clean broadcast. The run loop MUST still be alive to deliver
	// it. With no inner recover, the loop exited after the first panic and
	// this send sits in the channel buffer forever.
	s.Broadcast(Message{Type: "after", Data: "still-alive"})

	select {
	case msg := <-live.Send:
		if msg.Type != "after" {
			t.Errorf("post-panic broadcast: expected type 'after', got %q", msg.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run loop did not deliver post-panic broadcast; consumer goroutine died")
	}

	// s.running must still be true; Start was never re-invoked.
	s.mu.RLock()
	running := s.running
	s.mu.RUnlock()
	if !running {
		t.Error("s.running flipped to false after recovered panic")
	}

	// Drop the live client before Shutdown so the connection-close walk
	// does not deref the nil Conn pointer.
	s.mu.Lock()
	delete(s.clients, live.ID)
	s.mu.Unlock()
}
