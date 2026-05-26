package auth

import (
	"net"
	"net/http"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/internal/clientip"
)

// fakeTrustedProxyGuard is a minimal Guard that records the
// TrustedProxies snapshot it receives via the auth.TrustedProxiesReceiver
// interface so we can assert Manager propagates correctly.
type fakeTrustedProxyGuard struct {
	mu      sync.Mutex
	proxies []*net.IPNet
}

func (g *fakeTrustedProxyGuard) SetTrustedProxies(p []*net.IPNet) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.proxies = append([]*net.IPNet(nil), p...)
}

func (g *fakeTrustedProxyGuard) snapshot() []*net.IPNet {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]*net.IPNet, len(g.proxies))
	copy(out, g.proxies)
	return out
}

// Implement the Guard interface (no-op for everything besides
// SetTrustedProxies; nothing under test exercises these paths).
func (g *fakeTrustedProxyGuard) Check(*http.Request) bool { return false }
func (g *fakeTrustedProxyGuard) User(*http.Request) Authenticatable {
	return nil
}
func (g *fakeTrustedProxyGuard) ID(*http.Request) interface{} { return nil }
func (g *fakeTrustedProxyGuard) Login(http.ResponseWriter, *http.Request, Authenticatable, ...bool) error {
	return nil
}
func (g *fakeTrustedProxyGuard) LoginByID(http.ResponseWriter, *http.Request, interface{}, ...bool) error {
	return nil
}
func (g *fakeTrustedProxyGuard) Attempt(http.ResponseWriter, *http.Request, map[string]interface{}, ...bool) (bool, error) {
	return false, nil
}
func (g *fakeTrustedProxyGuard) Logout(http.ResponseWriter, *http.Request) error {
	return nil
}
func (g *fakeTrustedProxyGuard) SetProvider(UserProvider) {}

func TestManager_SetTrustedProxies_PropagatesToRegisteredGuards(t *testing.T) {
	m := NewManager()
	g := &fakeTrustedProxyGuard{}
	m.RegisterGuard("test", g)

	proxies, err := clientip.ParseCIDRs([]string{"10.0.0.0/8", "192.168.0.0/16"})
	if err != nil {
		t.Fatalf("ParseCIDRs: %v", err)
	}

	m.SetTrustedProxies(proxies)

	got := g.snapshot()
	if len(got) != 2 {
		t.Fatalf("guard did not receive proxies: len=%d", len(got))
	}
	mgrSnap := m.TrustedProxies()
	if len(mgrSnap) != 2 {
		t.Fatalf("manager snapshot wrong len: %d", len(mgrSnap))
	}
}

func TestManager_RegisterGuard_InheritsTrustedProxies(t *testing.T) {
	// SetTrustedProxies BEFORE RegisterGuard: guards registered later
	// must still inherit the list.
	m := NewManager()
	proxies, _ := clientip.ParseCIDRs([]string{"10.0.0.0/8"})
	m.SetTrustedProxies(proxies)

	g := &fakeTrustedProxyGuard{}
	m.RegisterGuard("test", g)

	if got := g.snapshot(); len(got) != 1 {
		t.Fatalf("guard registered after SetTrustedProxies did not inherit: len=%d", len(got))
	}
}

func TestManager_SetTrustedProxies_NilClears(t *testing.T) {
	m := NewManager()
	g := &fakeTrustedProxyGuard{}
	m.RegisterGuard("test", g)

	proxies, _ := clientip.ParseCIDRs([]string{"10.0.0.0/8"})
	m.SetTrustedProxies(proxies)
	m.SetTrustedProxies(nil)

	if got := m.TrustedProxies(); len(got) != 0 {
		t.Fatalf("nil set did not clear: %v", got)
	}
}

func TestManager_SetTrustedProxies_DefensiveCopy(t *testing.T) {
	// Caller-side mutation of the slice handed to SetTrustedProxies
	// must not affect Manager's view.
	m := NewManager()
	proxies, _ := clientip.ParseCIDRs([]string{"10.0.0.0/8"})
	m.SetTrustedProxies(proxies)

	// Stomp the slice contents.
	for i := range proxies {
		proxies[i] = nil
	}

	got := m.TrustedProxies()
	if len(got) != 1 || got[0] == nil {
		t.Fatalf("manager observed caller-side mutation: %v", got)
	}
}
