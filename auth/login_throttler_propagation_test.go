package auth

import (
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
)

type fakeLoginThrottlerReceiverGuard struct {
	mockGuard
	manager   *Manager
	mu        sync.Mutex
	throttler contract.LoginThrottler
}

func (g *fakeLoginThrottlerReceiverGuard) SetLoginThrottler(t contract.LoginThrottler) {
	if g.manager != nil {
		_, _ = g.manager.Guard("test")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.throttler = t
}

func (g *fakeLoginThrottlerReceiverGuard) snapshot() contract.LoginThrottler {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.throttler
}

func TestManagerSetLoginThrottlerPropagatesOutsideLock(t *testing.T) {
	m := NewManager()
	g := &fakeLoginThrottlerReceiverGuard{manager: m}
	m.RegisterGuard("test", g)

	throttler := NoopLoginThrottler{}
	done := make(chan struct{})
	go func() {
		m.SetLoginThrottler(throttler)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SetLoginThrottler did not return; receiver was likely called under Manager mutex")
	}

	if got := g.snapshot(); got == nil {
		t.Fatal("guard did not receive login throttler")
	}
}

func TestManagerRegisterGuardInheritsLoginThrottler(t *testing.T) {
	m := NewManager()
	throttler := NoopLoginThrottler{}
	m.SetLoginThrottler(throttler)

	g := &fakeLoginThrottlerReceiverGuard{}
	m.RegisterGuard("test", g)

	if got := g.snapshot(); got == nil {
		t.Fatal("guard registered after SetLoginThrottler did not inherit")
	}
}

func TestManagerSetLoginThrottlerSkipsNonReceivers(t *testing.T) {
	m := NewManager()
	m.RegisterGuard("test", &mockGuard{})

	m.SetLoginThrottler(NoopLoginThrottler{})
}

var _ Guard = (*fakeLoginThrottlerReceiverGuard)(nil)
var _ LoginThrottlerReceiver = (*fakeLoginThrottlerReceiverGuard)(nil)
var _ contract.LoginThrottler = NoopLoginThrottler{}
