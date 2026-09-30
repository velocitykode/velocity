package websocket

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
)

// A stopped server discards its fan-out backlog instead of delivering it
// to clients whose pumps have ended, and says so in one line; the broadcast
// whose delivery had begun finishes.
func TestShutdown_DiscardsTheFanoutBacklog(t *testing.T) {
	logs := hostile.NewLogger(nil)
	s := New(DefaultConfig())
	s.SetLogger(logs)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	s.fanoutHook = func() {
		once.Do(func() {
			close(entered)
			<-release
		})
	}
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	const clients, broadcasts = 8, 50
	cs := make([]*Client, clients)
	s.mu.Lock()
	for i := range cs {
		cs[i] = &Client{ID: fmt.Sprintf("c-%d", i), send: make(chan Message, broadcasts)}
		s.clients[cs[i].ID] = cs[i]
	}
	s.mu.Unlock()

	for range broadcasts {
		s.Broadcast(Message{Type: "backlog"})
	}
	<-entered
	// The run loop has taken every broadcast: the channel is empty, and a
	// registration it handles after that (it is serial) proves the last
	// one it took was queued for the held fan-out.
	hostile.Eventually(t, hostile.Deadline, "the run loop took every broadcast", func() bool {
		return len(s.broadcast) == 0
	})
	fence := &Client{ID: "fence", send: make(chan Message, 1), Groups: make(map[string]bool)}
	s.register <- fence
	hostile.Eventually(t, hostile.Deadline, "the fence registered", func() bool {
		_, ok := s.GetClient(fence.ID)
		return ok
	})

	// The broadcasts carry their client snapshots; the synthetic clients,
	// which have no connection for the drain to close, leave the registry
	// before the stop, as in the other snapshot tests.
	s.mu.Lock()
	for _, c := range cs {
		delete(s.clients, c.ID)
	}
	delete(s.clients, fence.ID)
	s.mu.Unlock()

	shut := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hostile.Deadline)
		defer cancel()
		shut <- s.Shutdown(ctx)
	}()
	hostile.Eventually(t, hostile.Deadline, "the server stopped", func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.stopped
	})
	close(release)
	if err := <-shut; err != nil {
		t.Fatalf("Shutdown = %v", err)
	}

	// The broadcast held in delivery began before the stop and finishes;
	// the backlog behind it is dropped.
	for _, c := range cs {
		if n := len(c.send); n > 1 {
			t.Errorf("client %s got %d broadcasts, want at most the one in delivery at the stop", c.ID, n)
		}
	}
	var discards []hostile.Line
	for _, l := range logs.Lines() {
		if l.Msg == "websocket: fan-out backlog discarded at shutdown" {
			discards = append(discards, l)
		}
	}
	if len(discards) != 1 {
		t.Fatalf("discard lines = %v, want 1", discards)
	}
}
