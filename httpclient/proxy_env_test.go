package httpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

// TestProxyEnvCleared_DefaultDeny pins the M-44 fix: when the SSRF dial
// guard is on (the default), the cloned default transport must have its
// ProxyFromEnvironment hook cleared so a hostile HTTP_PROXY env value
// cannot route outbound traffic through an attacker-controlled proxy
// that CONNECT-tunnels to a metadata service inside the trust boundary.
func TestProxyEnvCleared_DefaultDeny(t *testing.T) {
	c := New()
	tr, ok := c.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", c.client.Transport)
	}
	if tr.Proxy != nil {
		t.Error("default-deny client must have Proxy=nil so HTTP_PROXY env cannot tunnel outbound requests")
	}
}

// TestProxyEnvHonoured_WithProxyAllowed verifies the explicit opt-in
// option restores the standard library's ProxyFromEnvironment hook so
// callers running behind a trusted egress gateway still work.
func TestProxyEnvHonoured_WithProxyAllowed(t *testing.T) {
	c := New(WithProxyAllowed())
	tr, ok := c.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", c.client.Transport)
	}
	if tr.Proxy == nil {
		t.Error("WithProxyAllowed must leave the ProxyFromEnvironment hook installed")
	}
}

// TestProxyEnvHonoured_WithoutPrivateIPDeny confirms the legacy
// behaviour when the SSRF guard is explicitly disabled: env proxies
// pass through unmodified, matching pre-M-44 expectations.
func TestProxyEnvHonoured_WithoutPrivateIPDeny(t *testing.T) {
	c := New(WithoutPrivateIPDeny())
	tr, ok := c.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", c.client.Transport)
	}
	if tr.Proxy == nil {
		t.Error("WithoutPrivateIPDeny must leave the ProxyFromEnvironment hook installed for compatibility")
	}
}

// TestProxyEnvCleared_DoesNotDialProxy is the end-to-end behavioural
// proof of the M-44 fix. Even with HTTP_PROXY set in the process
// environment, the default-deny client must not route through it.
//
// Note on the env cache: http.ProxyFromEnvironment is gated by a
// process-wide sync.Once that captures HTTP(S)_PROXY at first read.
// Whether that cache fires or not is irrelevant here because the
// transport's Proxy hook is nil; the assertion is "proxy not dialled",
// which holds regardless of the cache state.
func TestProxyEnvCleared_DoesNotDialProxy(t *testing.T) {
	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()

	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")

	// Target a public-looking host that will fail to resolve / connect;
	// the assertion is on the proxy NOT being dialled. We pair the
	// default deny with an allowlist so the URL-host gate does not
	// short-circuit before the dial layer is exercised.
	c := New(WithAllowedHosts("example.invalid"))
	_, _ = c.Get(context.Background(), "http://example.invalid/")

	if hits := proxyHits.Load(); hits != 0 {
		t.Errorf("env proxy must not be dialled in default-deny mode; hits=%d", hits)
	}
}

// TestProxyEnvHonoured_WithProxyAllowed_DialsProxy proves the escape
// hatch reaches the configured proxy. The proxy URL is wired via an
// explicit transport rather than HTTP_PROXY env because the stdlib's
// sync.Once cache around envProxyFunc() makes env-driven proxy testing
// flaky across the suite. The branch under test is "WithProxyAllowed
// does NOT nil out the Proxy field"; the unit assertion in
// TestProxyEnvHonoured_WithProxyAllowed already pins the env path.
//
// The loopback host is whitelisted because the dial guard would
// otherwise refuse 127.0.0.1 (proxy and target both live on loopback
// here). In production the trusted egress proxy lives on a public or
// approved-private address, so the same guard is what stops a misconfig
// where HTTP_PROXY points at a metadata IP.
func TestProxyEnvHonoured_WithProxyAllowed_DialsProxy(t *testing.T) {
	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()

	proxyURL, _ := url.Parse(proxy.URL)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	c := New(
		WithProxyAllowed(),
		WithHTTPClient(&http.Client{Transport: transport}),
		WithAllowedHosts("127.0.0.1", "example.com"),
	)

	resp, err := c.Get(context.Background(), "http://example.com/")
	if err != nil {
		t.Fatalf("Get via proxy: %v", err)
	}
	resp.Body.Close()

	if hits := proxyHits.Load(); hits == 0 {
		t.Error("WithProxyAllowed must let the configured proxy be dialled")
	}
}

// TestProxyEnv_CustomTransportUnchanged confirms that supplying a
// custom transport via WithHTTPClient leaves the Proxy field as the
// caller set it. The fix only touches the default-construction path
// (base == nil in buildTransport).
func TestProxyEnv_CustomTransportUnchanged(t *testing.T) {
	custom := &http.Transport{Proxy: http.ProxyFromEnvironment}
	c := New(WithHTTPClient(&http.Client{Transport: custom}))
	tr, ok := c.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", c.client.Transport)
	}
	if tr.Proxy == nil {
		t.Error("caller-supplied transport must keep its Proxy field intact")
	}
}
