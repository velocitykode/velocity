package httpclient

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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
	// plumbing. The logic being validated is the critical unit.
	c := New()

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
	c := New()

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
