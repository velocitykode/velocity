package websocket

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/async"
	testsync "github.com/velocitykode/velocity/testing"
)

// TestServer_RunLoopPanicRecovered covers audit D-04: the run goroutine
// must be wrapped so a panic from handleRegister/Unregister/Broadcast does
// not propagate past the goroutine boundary and kill the process.
//
// The test triggers a `send on closed channel` panic inside handleBroadcast
// by inserting a synthetic client whose Send channel has been closed. With
// the async.Go wrapper installed, the panic is observed by the package
// panic hook and the test process keeps running; without the wrapper the
// process would terminate before the assertion runs.
func TestServer_RunLoopPanicRecovered(t *testing.T) {
	var panicCount atomic.Int32
	async.SetPanicHook(func(p any) {
		panicCount.Add(1)
	})
	t.Cleanup(func() { async.SetPanicHook(nil) })

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

	// If async.Go is NOT wrapping the run loop, the process dies before
	// this assertion completes; the test would never reach the next line.
	// If it IS wrapping, the package panic hook records the recovered
	// panic and we observe panicCount >= 1.
	testsync.Eventually(t, func() bool { return panicCount.Load() >= 1 }, 2*time.Second, "panic recovered by async hook")

	// Remove the synthetic client before Shutdown so the shutdown
	// connection-close walk does not deref the nil Conn pointer.
	s.mu.Lock()
	delete(s.clients, bait.ID)
	s.mu.Unlock()
}

// TestServer_RegisterPanicRecovered pins that a panic raised during
// handleRegister (here via a closed Send channel when handleRegister tries
// to enqueue the welcome message) is contained by the async.Go wrapper.
// Same shape as the broadcast test, exercising the register code path.
func TestServer_RegisterPanicRecovered(t *testing.T) {
	var panicCount atomic.Int32
	async.SetPanicHook(func(p any) {
		panicCount.Add(1)
	})
	t.Cleanup(func() { async.SetPanicHook(nil) })

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

	testsync.Eventually(t, func() bool { return panicCount.Load() >= 1 }, 2*time.Second, "register panic recovered")

	// Drop the synthetic client before Shutdown so the connection-close
	// walk does not deref the nil Conn pointer.
	s.mu.Lock()
	delete(s.clients, bad.ID)
	s.mu.Unlock()
}
