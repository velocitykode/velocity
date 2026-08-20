package httpclient

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPrivateIPDeny_Loopback(t *testing.T) {
	// A backend on 127.0.0.1 must be unreachable when the deny option is set.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("loopback backend must not be reached")
	}))
	defer backend.Close()

	c := New(WithPrivateIPDeny())
	_, err := c.Get(context.Background(), backend.URL)
	if err == nil {
		t.Fatal("expected private-IP deny to block 127.0.0.1")
	}
	if !errors.Is(err, errPrivateIP) {
		t.Errorf("expected errPrivateIP, got %v", err)
	}
}

func TestPrivateIPDeny_IPv6Loopback(t *testing.T) {
	c := New(WithPrivateIPDeny())
	_, err := c.Get(context.Background(), "http://[::1]:1/")
	if err == nil {
		t.Fatal("expected deny on ::1")
	}
	if !errors.Is(err, errPrivateIP) {
		t.Errorf("expected errPrivateIP, got %v", err)
	}
}

func TestPrivateIPDeny_MetadataIP(t *testing.T) {
	c := New(WithPrivateIPDeny())
	_, err := c.Get(context.Background(), "http://169.254.169.254/latest/meta-data/")
	if err == nil {
		t.Fatal("expected deny on AWS metadata IP")
	}
	if !errors.Is(err, errPrivateIP) {
		t.Errorf("expected errPrivateIP, got %v", err)
	}
}

func TestPrivateIPDeny_AllowedHost(t *testing.T) {
	// When the caller explicitly whitelists the eTLD+1 the deny is bypassed.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	c := New(WithPrivateIPDeny(), WithAllowedHosts("127.0.0.1"))
	resp, err := c.Get(context.Background(), backend.URL)
	if err != nil {
		t.Fatalf("expected allowlist to permit request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestCrossHostRedirect_StripsSensitiveHeaders(t *testing.T) {
	// Exercise checkRedirect directly: httptest binds only 127.0.0.1 so we
	// cannot stand up two hosts with different eTLD+1 values without DNS
	// plumbing. The logic being validated is the critical unit. Disable
	// the URL-host SSRF gate so the test does not depend on resolving
	// the placeholder hostnames against the real network.
	c := New(WithoutPrivateIPDeny())

	orig, _ := http.NewRequest("GET", "https://api.example.com/", nil)
	orig.Header.Set("Authorization", "Bearer secret")
	orig.Header.Set("Cookie", "sid=abc")
	orig.Header.Set("Proxy-Authorization", "Basic dXNlcjpwYXNz")

	next, _ := http.NewRequest("GET", "https://evil.test/", nil)
	next.Header = orig.Header.Clone()

	if err := c.checkRedirect(next, []*http.Request{orig}); err != nil {
		t.Fatalf("checkRedirect: %v", err)
	}
	if next.Header.Get("Authorization") != "" {
		t.Error("Authorization must be stripped on cross-host redirect")
	}
	if next.Header.Get("Cookie") != "" {
		t.Error("Cookie must be stripped on cross-host redirect")
	}
	if next.Header.Get("Proxy-Authorization") != "" {
		t.Error("Proxy-Authorization must be stripped on cross-host redirect")
	}
}

func TestSameHostRedirect_PreservesHeaders(t *testing.T) {
	c := New(WithoutPrivateIPDeny())

	orig, _ := http.NewRequest("GET", "https://api.example.com/a", nil)
	orig.Header.Set("Authorization", "Bearer secret")

	next, _ := http.NewRequest("GET", "https://api.example.com/b", nil)
	next.Header = orig.Header.Clone()

	if err := c.checkRedirect(next, []*http.Request{orig}); err != nil {
		t.Fatalf("checkRedirect: %v", err)
	}
	if next.Header.Get("Authorization") != "Bearer secret" {
		t.Error("Authorization must be preserved on same-host redirect")
	}
}

func TestRedirect_CrossSuffixAttackPair_StripsSensitiveHeaders(t *testing.T) {
	c := New(WithoutPrivateIPDeny())

	orig, _ := http.NewRequest("GET", "https://api.victim.co.uk/", nil)
	orig.Header.Set("Authorization", "Bearer secret")
	orig.Header.Set("Cookie", "sid=abc")
	orig.Header.Set("Proxy-Authorization", "Basic dXNlcjpwYXNz")

	next, _ := http.NewRequest("GET", "https://attacker.co.uk/", nil)
	next.Header = orig.Header.Clone()

	if err := c.checkRedirect(next, []*http.Request{orig}); err != nil {
		t.Fatalf("checkRedirect: %v", err)
	}
	for _, header := range sensitiveHeaders {
		if got := next.Header.Get(header); got != "" {
			t.Errorf("%s must be stripped for cross-suffix attack pair, got %q", header, got)
		}
	}
}

func TestRedirect_SameRegistrableDomain_PreservesSensitiveHeaders(t *testing.T) {
	c := New(WithoutPrivateIPDeny())

	orig, _ := http.NewRequest("GET", "https://api.example.com/", nil)
	orig.Header.Set("Authorization", "Bearer secret")
	orig.Header.Set("Cookie", "sid=abc")
	orig.Header.Set("Proxy-Authorization", "Basic dXNlcjpwYXNz")

	next, _ := http.NewRequest("GET", "https://app.example.com/", nil)
	next.Header = orig.Header.Clone()

	if err := c.checkRedirect(next, []*http.Request{orig}); err != nil {
		t.Fatalf("checkRedirect: %v", err)
	}
	for _, header := range sensitiveHeaders {
		if got := next.Header.Get(header); got != orig.Header.Get(header) {
			t.Errorf("%s = %q, want %q", header, got, orig.Header.Get(header))
		}
	}
}

func TestRedirect_CapsAtMax(t *testing.T) {
	c := New(WithMaxRedirects(3))

	via := []*http.Request{}
	for i := 0; i < 3; i++ {
		r, _ := http.NewRequest("GET", "https://example.com/", nil)
		via = append(via, r)
	}
	next, _ := http.NewRequest("GET", "https://example.com/next", nil)
	err := c.checkRedirect(next, via)
	if err == nil {
		t.Fatal("expected cap error")
	}
	if !strings.Contains(err.Error(), "stopped after 3 redirects") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRedirect_ZeroDisablesFollowing(t *testing.T) {
	c := New(WithMaxRedirects(0), WithoutPrivateIPDeny())
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/", http.StatusFound)
	}))
	defer backend.Close()

	resp, err := c.Get(context.Background(), backend.URL)
	if err != nil {
		t.Fatalf("did not expect error with ErrUseLastResponse behaviour: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("expected 302 returned directly, got %d", resp.StatusCode)
	}
}

func TestMinTLSVersion_DefaultIs12(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.TLS = &tls.Config{MaxVersion: tls.VersionTLS11}
	srv.StartTLS()
	defer srv.Close()

	serverClient := srv.Client()
	tr, ok := serverClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("httptest server.Client transport not *http.Transport")
	}
	roots := tr.TLSClientConfig.RootCAs

	c := New(WithHTTPClient(&http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: roots},
		},
	}))

	_, err := c.Get(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected TLS handshake failure when server maxes at TLS 1.1")
	}
}

func TestWithMinTLSVersion_ForcesTLS13(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.TLS = &tls.Config{MaxVersion: tls.VersionTLS12}
	srv.StartTLS()
	defer srv.Close()

	serverClient := srv.Client()
	tr := serverClient.Transport.(*http.Transport)
	roots := tr.TLSClientConfig.RootCAs

	c := New(
		WithMinTLSVersion(tls.VersionTLS13),
		WithHTTPClient(&http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{RootCAs: roots},
			},
		}),
	)

	_, err := c.Get(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected TLS 1.3 pin to reject TLS 1.2 server")
	}
}

func TestSameRedirectOrigin_DowngradeHTTPSToHTTP(t *testing.T) {
	https, _ := url.Parse("https://example.com/")
	httpURL, _ := url.Parse("http://example.com/")
	if sameRedirectOrigin(https, httpURL) {
		t.Error("https→http must be treated as cross-origin")
	}
}

func TestPrivateIPDeny_OnByDefault(t *testing.T) {
	// Constructed with no options: the deny must be active and the
	// loopback test backend unreachable. This is the inverted default.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("loopback backend must not be reached when guard is on by default")
	}))
	defer backend.Close()

	c := New()
	_, err := c.Get(context.Background(), backend.URL)
	if err == nil {
		t.Fatal("expected default-on private-IP deny to block 127.0.0.1")
	}
	if !errors.Is(err, errPrivateIP) {
		t.Errorf("expected errPrivateIP, got %v", err)
	}
}

func TestPrivateIPDeny_RFC1918_Blocked(t *testing.T) {
	// Address-level deny must block RFC1918 ranges even without a live
	// listener; the dial is refused before a TCP connection is made.
	c := New()
	for _, addr := range []string{
		"http://10.0.0.1/",
		"http://192.168.0.1/",
		"http://172.16.0.1/",
	} {
		_, err := c.Get(context.Background(), addr)
		if err == nil {
			t.Errorf("%s: expected deny, got nil error", addr)
			continue
		}
		if !errors.Is(err, errPrivateIP) {
			t.Errorf("%s: expected errPrivateIP, got %v", addr, err)
		}
	}
}

func TestPrivateIPDeny_LinkLocalAndCloudMetadata_Blocked(t *testing.T) {
	c := New()
	for _, addr := range []string{
		"http://169.254.1.1/",
		"http://169.254.169.254/latest/meta-data/",
		"http://[fe80::1]/",
	} {
		_, err := c.Get(context.Background(), addr)
		if err == nil {
			t.Errorf("%s: expected deny, got nil error", addr)
			continue
		}
		if !errors.Is(err, errPrivateIP) {
			t.Errorf("%s: expected errPrivateIP, got %v", addr, err)
		}
	}
}

func TestPrivateIPDeny_IPv6ULA_Blocked(t *testing.T) {
	// fc00::/7 is unique local addresses (RFC 4193).
	c := New()
	_, err := c.Get(context.Background(), "http://[fc00::1]/")
	if err == nil {
		t.Fatal("expected deny on fc00::/7")
	}
	if !errors.Is(err, errPrivateIP) {
		t.Errorf("expected errPrivateIP, got %v", err)
	}
}

func TestWithoutPrivateIPDeny_ReachesLoopback(t *testing.T) {
	// Explicit escape hatch must restore connectivity to a local
	// httptest server.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	c := New(WithoutPrivateIPDeny())
	resp, err := c.Get(context.Background(), backend.URL)
	if err != nil {
		t.Fatalf("WithoutPrivateIPDeny must allow loopback: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestMaxResponseBytes_CapsReads(t *testing.T) {
	// Server streams 64 MiB of zeroes in chunks. The client caps the
	// body at 1 KiB and the read must terminate with ErrResponseTooLarge
	// well before the host runs out of memory.
	const totalBytes = 64 << 20
	const chunkSize = 64 << 10

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Do not set Content-Length so the response is chunked. The
		// cap must hold without relying on an honest header.
		w.WriteHeader(http.StatusOK)
		buf := make([]byte, chunkSize)
		flusher, _ := w.(http.Flusher)
		written := 0
		for written < totalBytes {
			n, err := w.Write(buf)
			if err != nil {
				return
			}
			written += n
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer srv.Close()

	c := New(WithoutPrivateIPDeny(), WithMaxResponseBytes(1024))
	resp, err := c.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()

	got, err := io.ReadAll(resp.Body)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("expected ErrResponseTooLarge, got %v", err)
	}
	if int64(len(got)) > 1024 {
		t.Errorf("returned %d bytes, want <= 1024", len(got))
	}
}

func TestMaxResponseBytes_UnderCapSucceeds(t *testing.T) {
	body := strings.Repeat("a", 512)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(body))
	}))
	defer srv.Close()

	c := New(WithoutPrivateIPDeny(), WithMaxResponseBytes(1024))
	resp, err := c.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != body {
		t.Errorf("body = %q, want %q", got, body)
	}
}

func TestMaxResponseBytes_ExactlyAtCapSucceeds(t *testing.T) {
	// The +1 sentinel in the read loop must let exactly-cap-many bytes
	// through; only the (cap+1)th byte trips the limit.
	const cap = 1024
	body := strings.Repeat("a", cap)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(body))
	}))
	defer srv.Close()

	c := New(WithoutPrivateIPDeny(), WithMaxResponseBytes(cap))
	resp, err := c.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll at-cap must succeed: %v", err)
	}
	if len(got) != cap {
		t.Errorf("len(got) = %d, want %d", len(got), cap)
	}
}

func TestMaxResponseBytes_ZeroDisablesCap(t *testing.T) {
	// Passing 0 turns the cap off so trusted bulk transfers are still
	// possible. The body should be readable in full.
	body := strings.Repeat("a", 4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(body))
	}))
	defer srv.Close()

	c := New(WithoutPrivateIPDeny(), WithMaxResponseBytes(0))
	resp, err := c.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll with disabled cap: %v", err)
	}
	if len(got) != 4096 {
		t.Errorf("len(got) = %d, want 4096", len(got))
	}
}

func TestMaxResponseBytes_DefaultApplied(t *testing.T) {
	// Without WithMaxResponseBytes the client should still wrap the
	// body with the default cap so a non-configuring caller gets the
	// safe behaviour automatically.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello"))
	}))
	defer srv.Close()

	c := New(WithoutPrivateIPDeny())
	resp, err := c.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if _, ok := resp.Body.(*limitedBody); !ok {
		t.Errorf("default cap missing: body type = %T", resp.Body)
	}
}

// TestPrivateIPDeny_DialGuardError_MatchesThroughURLError proves that a
// blocked host caught by the dial-time guard (rather than the URL-host
// gate) still surfaces to callers in a matchable form. On the
// direct-dial path the gate skips DNS resolution, so a hostname that
// resolves to a private address is refused inside the transport; the
// stdlib wraps that refusal in *url.Error, and errors.Is / errors.As
// must match through the wrap.
func TestPrivateIPDeny_DialGuardError_MatchesThroughURLError(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("private backend must not be reached via hostname")
	}))
	defer backend.Close()

	// Reach the loopback backend via "localhost" so the gate's literal-IP
	// check does not fire and the deny comes from the dial guard's
	// resolution (localhost resolves via the hosts file, no network DNS).
	backendURL, _ := url.Parse(backend.URL)
	target := "http://localhost:" + backendURL.Port() + "/"

	c := New()
	_, err := c.Get(context.Background(), target)
	if err == nil {
		t.Fatal("expected dial guard to block localhost by resolution")
	}
	var uerr *url.Error
	if !errors.As(err, &uerr) {
		t.Fatalf("expected *url.Error from inside the transport, got %T: %v", err, err)
	}
	if !errors.Is(err, errPrivateIP) {
		t.Errorf("errors.Is must match errPrivateIP through *url.Error, got %v", err)
	}
}

// TestDirectDial_GateSkipsDNS proves the URL-host gate performs no DNS
// resolution on the direct-dial path: with a resolver that fails every
// query, the gate still passes a hostname URL (resolution is owned by
// the dial guard there), while the proxy-mode gate still resolves the
// upstream host and therefore surfaces the resolver failure.
func TestDirectDial_GateSkipsDNS(t *testing.T) {
	brokenResolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return nil, errors.New("gate must not resolve on direct dial")
		},
	}
	u, _ := url.Parse("http://direct-dial-check.test/")

	direct := New()
	direct.resolver = brokenResolver
	if !direct.directDial {
		t.Fatal("default construction must be direct-dial")
	}
	if err := direct.assertURLAllowed(context.Background(), u); err != nil {
		t.Errorf("direct-dial gate must skip DNS resolution, got %v", err)
	}

	proxied := New(WithProxyAllowed())
	proxied.resolver = brokenResolver
	if proxied.directDial {
		t.Fatal("proxy-allowed client must not be direct-dial")
	}
	if err := proxied.assertURLAllowed(context.Background(), u); err == nil {
		t.Error("proxy-mode gate must still resolve the upstream host")
	}
}

// TestDirectDial_Wiring pins down which constructions may skip the
// URL-gate DNS lookup. Only the framework-built guarded transport
// qualifies. Every WithHTTPClient construction keeps the gate's full
// check, whatever the supplied transport looks like at construction:
// the caller can later mutate Proxy, TLS dial hooks, or register
// alternate protocols, all of which route around the guarded
// DialContext.
func TestDirectDial_Wiring(t *testing.T) {
	if !New().directDial {
		t.Error("default construction should be direct-dial")
	}
	if New(WithProxyAllowed()).directDial {
		t.Error("WithProxyAllowed keeps the env proxy hook; not direct-dial")
	}
	if New(WithoutPrivateIPDeny()).directDial {
		t.Error("without the dial guard there is no direct-dial fast path")
	}

	proxyURL, _ := url.Parse("http://proxy.test:8080")
	withProxy := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	if New(WithHTTPClient(&http.Client{Transport: withProxy})).directDial {
		t.Error("explicit proxy transport must not be direct-dial")
	}

	if New(WithHTTPClient(&http.Client{Transport: &http.Transport{}})).directDial {
		t.Error("caller-owned transport is mutable after construction; not direct-dial")
	}
	if New(WithHTTPClient(&http.Client{})).directDial {
		t.Error("caller-owned client can swap Transport after construction; not direct-dial")
	}

	withDialTLSCtx := &http.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return nil, errors.New("unused")
		},
	}
	if New(WithHTTPClient(&http.Client{Transport: withDialTLSCtx})).directDial {
		t.Error("DialTLSContext bypasses the guarded DialContext for HTTPS; not direct-dial")
	}

	//lint:ignore SA1019 the deprecated DialTLS field still routes HTTPS around DialContext and must disable the fast path
	withDialTLS := &http.Transport{
		DialTLS: func(network, addr string) (net.Conn, error) {
			return nil, errors.New("unused")
		},
	}
	if New(WithHTTPClient(&http.Client{Transport: withDialTLS})).directDial {
		t.Error("DialTLS bypasses the guarded DialContext for HTTPS; not direct-dial")
	}

	custom := roundTripperFunc(func(r *http.Request) (*http.Response, error) { return nil, errors.New("unused") })
	if New(WithHTTPClient(&http.Client{Transport: custom})).directDial {
		t.Error("non-*http.Transport RoundTripper has no dial guard; not direct-dial")
	}
}

// roundTripperFunc adapts a function to http.RoundTripper for tests.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestPrivateIPDeny_ProxyModeBypassRefused_Env exercises the proxy
// bypass path with HTTP_PROXY set via env. It uses t.Setenv to keep
// the env scoped to this test. Note: http.ProxyFromEnvironment caches
// the first env read; the assertion here is on the URL-host gate's
// rejection regardless of whether the cached proxy func ever fires.
func TestPrivateIPDeny_ProxyModeBypassRefused_Env(t *testing.T) {
	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()

	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")

	c := New()
	_, err := c.Get(context.Background(), "http://10.0.0.5/secret")
	if err == nil {
		t.Fatal("expected URL-host gate to refuse private upstream behind proxy")
	}
	if !errors.Is(err, errPrivateIP) {
		t.Errorf("expected errPrivateIP, got %v", err)
	}
	if hits := proxyHits.Load(); hits != 0 {
		t.Errorf("proxy must not be dialled when upstream is private; hits=%d", hits)
	}
}

// TestPrivateIPDeny_ProxyModeBypassRefused_Explicit proves the URL-host
// gate fires before the transport even when a non-env proxy is wired
// in via WithHTTPClient. The dial guard alone would see the proxy's
// public IP and wave the request through; only the URL-host check
// catches it.
func TestPrivateIPDeny_ProxyModeBypassRefused_Explicit(t *testing.T) {
	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()

	proxyURL, _ := url.Parse(proxy.URL)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	c := New(WithHTTPClient(&http.Client{Transport: transport}))

	_, err := c.Get(context.Background(), "http://10.0.0.5/secret")
	if err == nil {
		t.Fatal("expected URL-host gate to refuse private upstream behind explicit proxy")
	}
	if !errors.Is(err, errPrivateIP) {
		t.Errorf("expected errPrivateIP, got %v", err)
	}
	if hits := proxyHits.Load(); hits != 0 {
		t.Errorf("proxy must not be dialled when upstream is private; hits=%d", hits)
	}
}

// TestPrivateIPDeny_RedirectToPrivate_Rejected ensures a public origin
// returning a 302 to an internal IP cannot trick the client into
// reaching that internal IP. The gate fires on every hop, including
// the redirect target.
func TestPrivateIPDeny_RedirectToPrivate_Rejected(t *testing.T) {
	// Public-style origin issues a 302 to a metadata IP.
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer origin.Close()

	// Allow the loopback origin so the first hop succeeds; the second
	// hop must still be rejected by the URL-host gate.
	c := New(WithAllowedHosts("127.0.0.1"))
	_, err := c.Get(context.Background(), origin.URL)
	if err == nil {
		t.Fatal("expected redirect to private IP to be rejected")
	}
	if !errors.Is(err, errPrivateIP) {
		t.Errorf("expected errPrivateIP, got %v", err)
	}
}

// TestPrivateIPDeny_RedirectToPrivate_RejectedUnderProxy is the proxy
// variant of the redirect test. The redirect target is internal; the
// gate must refuse to follow even though the dial path would only ever
// see the proxy.
func TestPrivateIPDeny_RedirectToPrivate_RejectedUnderProxy(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer origin.Close()

	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		// If we ever get here, forward to the origin to keep the
		// redirect chain live. The check below should never see hits
		// for the metadata IP though.
		http.Redirect(w, r, origin.URL, http.StatusFound)
	}))
	defer proxy.Close()

	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")

	// Use the origin URL directly so the first hop is the public-ish
	// origin (loopback, whitelisted) and the second hop is private.
	c := New(WithAllowedHosts("127.0.0.1"))
	_, err := c.Get(context.Background(), origin.URL)
	if err == nil {
		t.Fatal("expected private redirect target to be rejected under proxy")
	}
	if !errors.Is(err, errPrivateIP) {
		t.Errorf("expected errPrivateIP, got %v", err)
	}
}

// TestWithoutPrivateIPDeny_ProxyAndDirect confirms the escape hatch
// disables both the URL-host gate and the dial guard, so internal
// traffic flows in both direct and proxy modes.
//
// Note on proxy testing: http.ProxyFromEnvironment caches its config
// the first time it is read, so the proxy half of this test wires a
// per-client Proxy function via WithHTTPClient instead of relying on
// HTTP_PROXY env. The behaviour under test is the URL-host gate, not
// the env-loading code path.
func TestWithoutPrivateIPDeny_ProxyAndDirect(t *testing.T) {
	// Direct: internal address reachable when deny is off.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()

	c := New(WithoutPrivateIPDeny())
	resp, err := c.Get(context.Background(), backend.URL)
	if err != nil {
		t.Fatalf("direct loopback must succeed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}

	// Proxy: forwarded request reaches a private upstream when deny
	// is off. The proxy itself is on loopback so this fully exercises
	// proxy mode without any external network.
	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()

	proxyURL, _ := url.Parse(proxy.URL)
	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
	}
	c2 := New(WithoutPrivateIPDeny(), WithHTTPClient(&http.Client{Transport: transport}))
	resp2, err := c2.Get(context.Background(), "http://10.0.0.5/whatever")
	if err != nil {
		t.Fatalf("proxy-mode request must succeed with deny off: %v", err)
	}
	resp2.Body.Close()
	if hits := proxyHits.Load(); hits == 0 {
		t.Error("proxy should have been dialled with deny off")
	}
}
