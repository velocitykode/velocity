package httpclient

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

// fakeAAAAResolver returns a resolver whose every AAAA answer is addr and
// whose A answers are empty, served by a UDP responder on loopback. The
// resolver never talks to a real DNS server.
func fakeAAAAResolver(t *testing.T, addr string) *net.Resolver {
	t.Helper()
	var aaaa [16]byte
	copy(aaaa[:], net.ParseIP(addr).To16())

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var p dnsmessage.Parser
			hdr, err := p.Start(buf[:n])
			if err != nil {
				continue
			}
			q, err := p.Question()
			if err != nil {
				continue
			}
			b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: hdr.ID, Response: true, Authoritative: true})
			b.EnableCompression()
			_ = b.StartQuestions()
			_ = b.Question(q)
			_ = b.StartAnswers()
			if q.Type == dnsmessage.TypeAAAA {
				_ = b.AAAAResource(dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, TTL: 1}, dnsmessage.AAAAResource{AAAA: aaaa})
			}
			out, err := b.Finish()
			if err != nil {
				continue
			}
			_, _ = pc.WriteTo(out, from)
		}
	}()
	t.Cleanup(func() {
		_ = pc.Close()
		<-done
	})

	server := pc.LocalAddr().String()
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "udp", server)
		},
	}
}

// TestPrivateIPDeny_EmbeddedIPv4Literal_Blocked covers IPv6 literals that
// carry an internal IPv4 address: the request is refused before any
// connection is attempted.
func TestPrivateIPDeny_EmbeddedIPv4Literal_Blocked(t *testing.T) {
	c := New()
	for _, target := range []string{
		"http://[64:ff9b::7f00:1]/",
		"http://[64:ff9b::a9fe:a9fe]/latest/meta-data/",
		"http://[::127.0.0.1]:8080/",
		"http://[::ffff:0:10.0.0.1]/",
		"http://[2002:7f00:1::]/",
		"http://[2002:a9fe:a9fe::]/",
		"http://[2606:4700::5efe:192.168.1.1]/",
	} {
		_, err := c.Get(context.Background(), target)
		if err == nil {
			t.Errorf("%s: expected deny, got nil error", target)
			continue
		}
		if !errors.Is(err, errPrivateIP) {
			t.Errorf("%s: expected errPrivateIP, got %v", target, err)
		}
	}
}

// TestPrivateIPDeny_EmbeddedIPv4Literal_DialGuard drives the dial guard
// directly, the enforcement point for a connection the URL gate did not
// see, and proves the wrapped dialer is never reached.
func TestPrivateIPDeny_EmbeddedIPv4Literal_DialGuard(t *testing.T) {
	c := New()
	var dialed atomic.Int32
	guarded := c.dialContextGuarded(func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialed.Add(1)
		return nil, errors.New("inner dialer must not run")
	})
	for _, addr := range []string{"[64:ff9b::7f00:1]:80", "[2002:7f00:1::]:443", "[::127.0.0.1]:80"} {
		_, err := guarded(context.Background(), "tcp", addr)
		if !errors.Is(err, errPrivateIP) {
			t.Errorf("%s: expected errPrivateIP, got %v", addr, err)
		}
	}
	if n := dialed.Load(); n != 0 {
		t.Errorf("inner dialer ran %d times for refused addresses", n)
	}
}

// TestPrivateIPDeny_ResolvesToEmbeddedIPv4_Blocked covers a hostname
// whose only address is a NAT64 address carrying loopback: the dial
// guard resolves it and refuses, on the request path and when called
// directly.
func TestPrivateIPDeny_ResolvesToEmbeddedIPv4_Blocked(t *testing.T) {
	for _, addr := range []string{"64:ff9b::7f00:1", "64:ff9b::a9fe:a9fe", "2002:a00:1::1"} {
		t.Run(addr, func(t *testing.T) {
			c := New()
			c.resolver = fakeAAAAResolver(t, addr)

			_, err := c.Get(context.Background(), "http://embedded-internal.test/")
			if err == nil {
				t.Fatal("expected deny for a host resolving to an embedded internal address")
			}
			if !errors.Is(err, errPrivateIP) {
				t.Fatalf("expected errPrivateIP, got %v", err)
			}

			var dialed atomic.Int32
			guarded := c.dialContextGuarded(func(ctx context.Context, network, addr string) (net.Conn, error) {
				dialed.Add(1)
				return nil, errors.New("inner dialer must not run")
			})
			if _, err := guarded(context.Background(), "tcp", "embedded-internal.test:80"); !errors.Is(err, errPrivateIP) {
				t.Errorf("dial guard: expected errPrivateIP, got %v", err)
			}
			if n := dialed.Load(); n != 0 {
				t.Errorf("inner dialer ran %d times", n)
			}
		})
	}
}

// TestPrivateIPDeny_ResolvesToEmbeddedPublicIPv4_Allowed is the other
// half: on an IPv6-only network with DNS64 every public IPv4 site
// resolves under the NAT64 prefix, and those must stay reachable. The
// guard passes the address and pins it for the dial.
func TestPrivateIPDeny_ResolvesToEmbeddedPublicIPv4_Allowed(t *testing.T) {
	c := New()
	c.resolver = fakeAAAAResolver(t, "64:ff9b::808:808")

	var got atomic.Value
	stop := errors.New("stop after the guard")
	guarded := c.dialContextGuarded(func(ctx context.Context, network, addr string) (net.Conn, error) {
		got.Store(addr)
		return nil, stop
	})
	_, err := guarded(context.Background(), "tcp", "dns64-public.test:443")
	if !errors.Is(err, stop) {
		t.Fatalf("expected the inner dialer to run, got %v", err)
	}
	if addr, _ := got.Load().(string); addr != "[64:ff9b::808:808]:443" {
		t.Errorf("dialed %q, want the pinned NAT64 address", addr)
	}

	c2 := New()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://[64:ff9b::808:808]/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c2.assertURLAllowed(req.Context(), req.URL); err != nil {
		t.Errorf("a NAT64 literal carrying a public address must pass the URL gate, got %v", err)
	}
}

// TestPrivateIPDeny_EmbeddedIPv4_ErrorText keeps the refusal
// text to the host the caller asked for and the address it resolved to.
func TestPrivateIPDeny_EmbeddedIPv4_ErrorText(t *testing.T) {
	c := New()
	_, err := c.Get(context.Background(), "http://[64:ff9b::7f00:1]/")
	if err == nil {
		t.Fatal("expected deny")
	}
	if !strings.Contains(err.Error(), "64:ff9b::7f00:1") {
		t.Errorf("refusal should name the requested host, got %v", err)
	}
}

// TestPrivateIPDeny_NAT64LocalUsePool_Blocked covers 64:ff9b:1::/48: the
// pool is refused whatever it appears to carry, as a literal and as a
// resolved address.
func TestPrivateIPDeny_NAT64LocalUsePool_Blocked(t *testing.T) {
	c := New()
	for _, target := range []string{
		"http://[64:ff9b:1::7f00:1]/",
		"http://[64:ff9b:1::808:808]/",
		"http://[64:ff9b:1:abcd::808:808]/",
	} {
		_, err := c.Get(context.Background(), target)
		if !errors.Is(err, errPrivateIP) {
			t.Errorf("%s: expected errPrivateIP, got %v", target, err)
		}
	}

	c.resolver = fakeAAAAResolver(t, "64:ff9b:1::808:808")
	_, err := c.Get(context.Background(), "http://local-use-pool.test/")
	if !errors.Is(err, errPrivateIP) {
		t.Errorf("resolved into the pool: expected errPrivateIP, got %v", err)
	}
}

// TestPrivateIPDeny_NAT64LocalUsePool_AllowedHosts shows the way out for
// an operator whose translator lives in the pool: an allowlisted
// hostname is dialed without the address check, and an allowlisted
// literal in canonical form passes too.
func TestPrivateIPDeny_NAT64LocalUsePool_AllowedHosts(t *testing.T) {
	stop := errors.New("stop after the guard")
	dial := func(c *Client, addr string) (string, error) {
		var got atomic.Value
		guarded := c.dialContextGuarded(func(ctx context.Context, network, addr string) (net.Conn, error) {
			got.Store(addr)
			return nil, stop
		})
		_, err := guarded(context.Background(), "tcp", addr)
		s, _ := got.Load().(string)
		return s, err
	}

	byName := New(WithAllowedHosts("partner.test"))
	byName.resolver = fakeAAAAResolver(t, "64:ff9b:1::808:808")
	if got, err := dial(byName, "api.partner.test:443"); !errors.Is(err, stop) || got != "api.partner.test:443" {
		t.Errorf("allowlisted hostname: dialed %q, err %v", got, err)
	}
	if _, err := dial(byName, "other.test:443"); !errors.Is(err, errPrivateIP) {
		t.Errorf("a host off the allowlist must still be refused, got %v", err)
	}

	byLiteral := New(WithAllowedHosts("64:ff9b:1::808:808"))
	if got, err := dial(byLiteral, "[64:ff9b:1::808:808]:443"); !errors.Is(err, stop) || got != "[64:ff9b:1::808:808]:443" {
		t.Errorf("allowlisted literal: dialed %q, err %v", got, err)
	}
	if _, err := dial(byLiteral, "[64:ff9b:1::808:809]:443"); !errors.Is(err, errPrivateIP) {
		t.Errorf("a neighbouring address must still be refused, got %v", err)
	}
}
