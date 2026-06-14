package websocket

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// startTestServer spins up a Server with the run loop active and registers a
// Shutdown cleanup. Synthetic clients are injected directly into s.clients (as
// the panic-recovery tests do) so the broadcast fan-out can be exercised
// without real websocket connections.
func startTestServer(t *testing.T) *Server {
	t.Helper()
	s := New(DefaultConfig())
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})
	return s
}

// TestHandleBroadcast_DeliversToSnapshotClients pins the correctness invariant
// the snapshot-then-send refactor must preserve: every client connected at
// snapshot time receives the message.
func TestHandleBroadcast_DeliversToSnapshotClients(t *testing.T) {
	s := startTestServer(t)

	const n = 16
	clients := make([]*Client, n)
	s.mu.Lock()
	for i := range clients {
		c := &Client{ID: fmt.Sprintf("c-%d", i), Send: make(chan Message, 1)}
		clients[i] = c
		s.clients[c.ID] = c
	}
	s.mu.Unlock()

	s.Broadcast(Message{Type: "snap", Data: "hello"})

	deadline := time.After(2 * time.Second)
	for i, c := range clients {
		select {
		case msg := <-c.Send:
			if msg.Type != "snap" {
				t.Errorf("client %d: expected type 'snap', got %q", i, msg.Type)
			}
		case <-deadline:
			t.Fatalf("client %d never received broadcast", i)
		}
	}

	s.mu.Lock()
	for _, c := range clients {
		delete(s.clients, c.ID)
	}
	s.mu.Unlock()
}

// TestHandleBroadcast_RegistrationProceedsDuringFanout pins the fix for the
// run-loop-serialization finding: the broadcast fan-out runs on a dedicated
// goroutine, so the run loop keeps draining s.register while a fan-out is in
// flight. The test holds a fan-out open via fanoutHook, then enqueues a real
// new client on s.register and asserts it lands in s.clients BEFORE the
// fan-out is released. Before the fix the send loop ran inline on the run-loop
// goroutine, so the queued client could not be registered until the fan-out
// finished.
func TestHandleBroadcast_RegistrationProceedsDuringFanout(t *testing.T) {
	s := New(DefaultConfig())

	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	// Set before Start so the fanout goroutine observes it race-free.
	s.fanoutHook = func() {
		once.Do(func() { close(started) })
		<-release
	}

	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// One pre-existing client so the broadcast has a real snapshot to deliver.
	early := &Client{ID: "early", Send: make(chan Message, 1), Groups: make(map[string]bool)}
	s.mu.Lock()
	s.clients[early.ID] = early
	s.mu.Unlock()

	// Kick off a broadcast; the fanout goroutine will block inside fanoutHook.
	s.Broadcast(Message{Type: "snap"})

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("fan-out never started")
	}

	// Fan-out is now paused. Enqueue a brand new client exactly as
	// HandleConnection does (s.register <- client) and assert the run loop
	// registers it while the fan-out is still blocked.
	late := &Client{ID: "late", Send: make(chan Message, 1), Groups: make(map[string]bool)}
	s.register <- late

	registered := false
	deadline := time.After(2 * time.Second)
	for !registered {
		if _, ok := s.GetClient("late"); ok {
			registered = true
			break
		}
		select {
		case <-deadline:
			t.Fatal("new client was not registered while the fan-out was paused (run loop serialized behind fan-out)")
		case <-time.After(time.Millisecond):
		}
	}

	// Release the fan-out and let it complete.
	close(release)

	// Remove synthetic clients before Shutdown so the connection-close walk
	// does not deref their nil Conn pointers.
	s.mu.Lock()
	delete(s.clients, early.ID)
	delete(s.clients, late.ID)
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.Shutdown(ctx)
}

// TestHandleBroadcast_RunLoopUnblockedWhenFanoutQueueFull pins the fix for the
// fan-out backpressure finding: the run loop's handoff to the fanout goroutine
// must never block, even when the fan-out is paused and the pending-job queue
// has grown far past the old 256-slot bound. With the previous bounded
// `fanout chan broadcastJob`, the 256th queued job blocked the run loop's send,
// so register/unregister stopped draining during a fan-out backlog. The test
// pauses the fan-out, floods the queue past 256, then proves a queued
// registration is still processed before the fan-out is released.
func TestHandleBroadcast_RunLoopUnblockedWhenFanoutQueueFull(t *testing.T) {
	s := New(DefaultConfig())

	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	// Set before Start so the fanout goroutine observes it race-free.
	s.fanoutHook = func() {
		once.Do(func() { close(started) })
		<-release
	}

	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// One pre-existing client so the broadcast has a real snapshot to deliver
	// and the hook fires.
	early := &Client{ID: "early", Send: make(chan Message, 1), Groups: make(map[string]bool)}
	s.mu.Lock()
	s.clients[early.ID] = early
	s.mu.Unlock()

	// First broadcast: the fanout goroutine grabs it and blocks inside fanoutHook.
	s.Broadcast(Message{Type: "snap"})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("fan-out never started")
	}

	// Flood far past the old 256-slot fanout bound while the fan-out is paused.
	// With the bounded channel the run loop blocked on the 256th handoff; the
	// unbounded queue must absorb them all without wedging run().
	const flood = 512
	for i := 0; i < flood; i++ {
		s.Broadcast(Message{Type: "flood"})
	}

	// Wait until the run loop has drained s.broadcast into the fanout queue,
	// confirming the queue is backed up well past the old bound - i.e. the run
	// loop kept moving instead of blocking on a full handoff.
	deadline := time.After(2 * time.Second)
	for {
		s.fanoutMu.Lock()
		n := len(s.fanoutQ)
		s.fanoutMu.Unlock()
		if n >= 256 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("fanout queue never filled past 256 (run loop stalled on handoff?); got %d", n)
		case <-time.After(time.Millisecond):
		}
	}

	// With the queue saturated and the fan-out still paused, a queued
	// registration must still be processed by the run loop.
	late := &Client{ID: "late", Send: make(chan Message, 1), Groups: make(map[string]bool)}
	s.register <- late

	registered := false
	regDeadline := time.After(2 * time.Second)
	for !registered {
		if _, ok := s.GetClient("late"); ok {
			registered = true
			break
		}
		select {
		case <-regDeadline:
			t.Fatal("registration blocked behind a saturated fanout queue (run loop wedged on handoff)")
		case <-time.After(time.Millisecond):
		}
	}

	// Release the fan-out and let it drain the backlog.
	close(release)

	// Remove synthetic clients before Shutdown so the connection-close walk does
	// not deref their nil Conn pointers.
	s.mu.Lock()
	delete(s.clients, early.ID)
	delete(s.clients, late.ID)
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.Shutdown(ctx)
}

// TestHandleBroadcast_ConcurrentRegistrationNotBlocked is the core regression
// test for the snapshot-then-send fix. Before the fix handleBroadcast held
// s.mu.RLock across the entire fan-out loop, so every s.mu writer (JoinGroup,
// LeaveGroup, ...) contended on the lock for the duration of the send. After the
// fix the lock is released the moment the client slice is copied.
//
// The test streams broadcasts to a large client set from one goroutine while
// other goroutines hammer writer-lock paths (JoinGroup/LeaveGroup) and a
// reader-lock path (GetClients). It asserts the writer ops keep making progress
// concurrently with the broadcast storm and that nothing panics or deadlocks.
// Run under `go test ./websocket -race` to catch any unsynchronised access to
// the snapshot or the client map.
func TestHandleBroadcast_ConcurrentRegistrationNotBlocked(t *testing.T) {
	s := startTestServer(t)

	// Large recipient set so each fan-out has real work to do; buffered Send so
	// the non-blocking select never drops under steady drain.
	const n = 2000
	ids := make([]string, n)
	s.mu.Lock()
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("bcast-%d", i)
		ids[i] = id
		s.clients[id] = &Client{ID: id, Send: make(chan Message, 256), Groups: make(map[string]bool)}
	}
	s.mu.Unlock()

	// Drain every client's Send so buffers never wedge during the storm.
	stopDrain := make(chan struct{})
	var drainWG sync.WaitGroup
	drainWG.Add(1)
	go func() {
		defer drainWG.Done()
		s.mu.RLock()
		chans := make([]chan Message, 0, len(s.clients))
		for _, c := range s.clients {
			chans = append(chans, c.Send)
		}
		s.mu.RUnlock()
		for {
			select {
			case <-stopDrain:
				return
			default:
			}
			for _, ch := range chans {
				select {
				case <-ch:
				default:
				}
			}
		}
	}()

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Broadcast storm.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				s.Broadcast(Message{Type: "storm"})
			}
		}
	}()

	// Writer-lock workers. join/leave count proves these paths keep advancing
	// while the broadcast storm runs - i.e. registration is not blocked behind
	// the fan-out.
	var joinLeave int64
	const writers = 4
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			id := ids[w*100]
			grp := fmt.Sprintf("g-%d", w)
			for {
				select {
				case <-stop:
					return
				default:
					if err := s.JoinGroup(id, grp); err != nil {
						t.Errorf("JoinGroup: %v", err)
						return
					}
					if err := s.LeaveGroup(id, grp); err != nil {
						t.Errorf("LeaveGroup: %v", err)
						return
					}
					atomic.AddInt64(&joinLeave, 1)
				}
			}
		}(w)
	}

	// Reader-lock worker.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = s.GetClients()
			}
		}
	}()

	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()
	close(stopDrain)
	drainWG.Wait()

	if got := atomic.LoadInt64(&joinLeave); got < int64(writers) {
		t.Fatalf("writer-lock paths starved during broadcast storm: only %d join/leave cycles", got)
	}
	t.Logf("join/leave cycles during broadcast storm: %d", atomic.LoadInt64(&joinLeave))

	s.mu.Lock()
	for _, id := range ids {
		delete(s.clients, id)
	}
	s.mu.Unlock()
}
