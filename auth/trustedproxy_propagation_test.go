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

// TestManager_SetTrustedProxies_DeepClone pins the C-05 follow-up fix:
// mutation of an IPNet's IP / Mask byte arrays AFTER SetTrustedProxies
// must not flip the manager's trust decisions. A shallow []*net.IPNet
// copy would reuse the same pointers and re-expose the audit hole.
func TestManager_SetTrustedProxies_DeepClone(t *testing.T) {
	m := NewManager()
	proxies, err := clientip.ParseCIDRs([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatalf("ParseCIDRs: %v", err)
	}
	m.SetTrustedProxies(proxies)

	// Mutate the IP bytes of the original to point at a completely
	// different network. With a deep clone, the manager's stored
	// IPNet must remain 10.0.0.0/8.
	for i := range proxies[0].IP {
		proxies[0].IP[i] = 0xff
	}
	// Also stomp the mask: open it up to /0.
	for i := range proxies[0].Mask {
		proxies[0].Mask[i] = 0
	}

	got := m.TrustedProxies()
	if len(got) != 1 || got[0] == nil {
		t.Fatalf("manager view lost: %v", got)
	}
	if !got[0].Contains(net.ParseIP("10.0.0.5")) {
		t.Errorf("manager forgot 10.0.0.5 (post-clone mutation of source leaked): got %v", got[0])
	}
	if got[0].Contains(net.ParseIP("203.0.113.5")) {
		t.Errorf("manager now trusts 203.0.113.5 (post-clone mutation leaked): got %v", got[0])
	}
}

// TestManager_SetTrustedProxies_AppendNotVisible: appending to the
// outer slice after SetTrustedProxies must not show up in the
// manager's snapshot.
func TestManager_SetTrustedProxies_AppendNotVisible(t *testing.T) {
	m := NewManager()
	proxies, _ := clientip.ParseCIDRs([]string{"10.0.0.0/8"})
	m.SetTrustedProxies(proxies)

	extra, _ := clientip.ParseCIDRs([]string{"192.168.0.0/16"})
	_ = append(proxies, extra...)

	got := m.TrustedProxies()
	if len(got) != 1 {
		t.Fatalf("manager length changed after append: %d", len(got))
	}
	if got[0].Contains(net.ParseIP("192.168.1.1")) {
		t.Errorf("manager picked up appended entry: %v", got[0])
	}
}

// TestManager_TrustedProxies_ReturnsClone: mutating the slice
// returned by TrustedProxies() must not affect subsequent reads.
func TestManager_TrustedProxies_ReturnsClone(t *testing.T) {
	m := NewManager()
	proxies, _ := clientip.ParseCIDRs([]string{"10.0.0.0/8"})
	m.SetTrustedProxies(proxies)

	snap := m.TrustedProxies()
	// Stomp the snapshot's IP bytes.
	for i := range snap[0].IP {
		snap[0].IP[i] = 0xff
	}

	again := m.TrustedProxies()
	if len(again) != 1 {
		t.Fatalf("second read length: %d", len(again))
	}
	if !again[0].Contains(net.ParseIP("10.0.0.5")) {
		t.Errorf("second read lost the original IP: %v", again[0])
	}
}
