// Package httpclient provides an instrumented HTTP client for APM monitoring.
// It wraps the standard http.Client and dispatches events for outgoing requests.
package httpclient

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/velocitykode/velocity/internal/neturl"
)

// errPrivateIP is the sentinel returned by the SSRF-guard DialContext
// when a resolved destination falls inside a disallowed private range.
// Wrapped with the velocity/httpclient prefix before surfacing.
var errPrivateIP = errors.New("velocity/httpclient: destination address is private or internal")

// defaultMaxRedirects caps redirect chains. The stdlib default is 10;
// the framework explicitly enforces the same cap so WithMaxRedirects can
// tighten it.
const defaultMaxRedirects = 10

// sensitiveHeaders are stripped on cross-host (eTLD+1) redirects to
// prevent leaking credentials to untrusted origins.
var sensitiveHeaders = []string{
	"Authorization",
	"Cookie",
	"Proxy-Authorization",
}

// Client is an instrumented HTTP client that dispatches events for APM monitoring
type Client struct {
	mu              sync.RWMutex
	client          *http.Client
	baseURL         string
	eventDispatcher func(event interface{}) error

	// Security options (configured via Option funcs).
	minTLSVersion   uint16
	maxRedirects    int
	denyPrivateIPs  bool
	allowedHosts    map[string]struct{} // eTLD+1 allowlist for private-IP deny
	resolver        *net.Resolver
	customTransport bool // set when WithHTTPClient supplies its own Transport
}

// Option configures a Client
type Option func(*Client)

// WithHTTPClient sets a custom http.Client. When used, TLS/redirect/SSRF
// options applied after this call take effect on the caller's transport
// only if it is an *http.Transport — otherwise they are ignored.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Client) {
		c.client = client
		c.customTransport = true
	}
}

// WithBaseURL sets a base URL for all requests
func WithBaseURL(baseURL string) Option {
	return func(c *Client) {
		c.baseURL = baseURL
	}
}

// WithTimeout sets the client timeout
func WithTimeout(timeout time.Duration) Option {
	return func(c *Client) {
		c.client.Timeout = timeout
	}
}

// WithMinTLSVersion forces a minimum TLS version on the default transport.
// Accepts tls.VersionTLS12 / tls.VersionTLS13 (the framework default is
// tls.VersionTLS12). Ignored when WithHTTPClient supplies a transport
// that is not *http.Transport.
func WithMinTLSVersion(v uint16) Option {
	return func(c *Client) {
		c.minTLSVersion = v
	}
}

// WithMaxRedirects caps the number of redirects a request will follow.
// Values <= 0 disable redirect following entirely.
func WithMaxRedirects(n int) Option {
	return func(c *Client) {
		c.maxRedirects = n
	}
}

// WithPrivateIPDeny installs a DialContext that refuses to connect to
// addresses in private, loopback, link-local, CGNAT, or cloud-metadata
// ranges. Combines with WithAllowedHosts for a per-client allowlist.
func WithPrivateIPDeny() Option {
	return func(c *Client) {
		c.denyPrivateIPs = true
	}
}

// WithAllowedHosts whitelists specific eTLD+1 hosts from the private-IP
// deny list — useful when you legitimately need to reach an internal
// service while still blocking everything else.
func WithAllowedHosts(hosts ...string) Option {
	return func(c *Client) {
		if c.allowedHosts == nil {
			c.allowedHosts = make(map[string]struct{}, len(hosts))
		}
		for _, h := range hosts {
			c.allowedHosts[strings.ToLower(h)] = struct{}{}
		}
	}
}

// New creates a new instrumented HTTP client with secure defaults:
// TLS >= 1.2, capped redirect chain, sensitive headers stripped on
// cross-host redirects. Add WithPrivateIPDeny for full SSRF hardening.
func New(opts ...Option) *Client {
	c := &Client{
		client:        &http.Client{Timeout: 30 * time.Second},
		minTLSVersion: tls.VersionTLS12,
		maxRedirects:  defaultMaxRedirects,
		resolver:      net.DefaultResolver,
	}

	for _, opt := range opts {
		opt(c)
	}

	// Only install a default transport when the caller did not provide one.
	// For caller-provided transports we still try to apply TLS minimum and
	// DialContext in-place if the transport is a plain *http.Transport.
	if !c.customTransport {
		c.client.Transport = c.buildTransport(nil)
	} else {
		if t, ok := c.client.Transport.(*http.Transport); ok {
			c.client.Transport = c.buildTransport(t)
		} else if c.client.Transport == nil {
			c.client.Transport = c.buildTransport(nil)
		}
	}

	c.client.CheckRedirect = c.checkRedirect

	return c
}

// buildTransport returns a transport with TLS minimum and SSRF-guard
// DialContext applied. If base is non-nil, its pooling/idle-timeout
// settings are preserved.
func (c *Client) buildTransport(base *http.Transport) *http.Transport {
	t := base
	if t == nil {
		t = http.DefaultTransport.(*http.Transport).Clone()
	}
	if t.TLSClientConfig == nil {
		t.TLSClientConfig = &tls.Config{}
	}
	if c.minTLSVersion != 0 && t.TLSClientConfig.MinVersion < c.minTLSVersion {
		t.TLSClientConfig.MinVersion = c.minTLSVersion
	}
	if c.denyPrivateIPs {
		t.DialContext = c.dialContextGuarded(t.DialContext)
	}
	return t
}

// dialContextGuarded wraps dial so every outbound TCP connection is
// resolved ahead of time and rejected if any resolved address falls in a
// disallowed private range. The resolved IP is pinned into the dial to
// prevent TOCTOU / DNS-rebinding between this check and the real dial.
func (c *Client) dialContextGuarded(inner func(ctx context.Context, network, addr string) (net.Conn, error)) func(ctx context.Context, network, addr string) (net.Conn, error) {
	if inner == nil {
		d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
		inner = d.DialContext
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("velocity/httpclient: split host/port: %w", err)
		}
		// Allowlist short-circuits the deny for a specific eTLD+1.
		if _, allowed := c.allowedHosts[strings.ToLower(neturl.ETLDPlusOne(host))]; allowed {
			return inner(ctx, network, addr)
		}
		if ip := net.ParseIP(host); ip != nil {
			if neturl.IsPrivateOrInternal(ip) {
				return nil, fmt.Errorf("velocity/httpclient: refusing to dial %s: %w", host, errPrivateIP)
			}
			return inner(ctx, network, addr)
		}
		addrs, err := c.resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("velocity/httpclient: resolve %s: %w", host, err)
		}
		if len(addrs) == 0 {
			return nil, fmt.Errorf("velocity/httpclient: resolve %s: no addresses", host)
		}
		for _, a := range addrs {
			if neturl.IsPrivateOrInternal(a.IP) {
				return nil, fmt.Errorf("velocity/httpclient: refusing to dial %s (resolves to %s): %w", host, a.IP, errPrivateIP)
			}
		}
		// Pin first resolved address to prevent re-resolution between check and dial.
		pinned := net.JoinHostPort(addrs[0].IP.String(), port)
		return inner(ctx, network, pinned)
	}
}

// checkRedirect is the stdlib CheckRedirect callback. It caps the chain
// length and strips sensitive headers when the next host (eTLD+1)
// differs from the original request host. Context from the first request
// is preserved automatically by net/http.
func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if c.maxRedirects <= 0 {
		return http.ErrUseLastResponse
	}
	if len(via) >= c.maxRedirects {
		return fmt.Errorf("velocity/httpclient: stopped after %d redirects", len(via))
	}
	if len(via) == 0 {
		return nil
	}
	original := via[0].URL
	if !sameRedirectOrigin(original, req.URL) {
		for _, h := range sensitiveHeaders {
			req.Header.Del(h)
		}
	}
	return nil
}

// sameRedirectOrigin returns true when two URLs share the same eTLD+1
// host. Scheme changes from https→http are treated as cross-origin to
// avoid silently downgrading credentials onto an http hop.
func sameRedirectOrigin(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	if a.Scheme == "https" && b.Scheme != "https" {
		return false
	}
	return neturl.ETLDPlusOne(a.Host) == neturl.ETLDPlusOne(b.Host)
}

// Do sends an HTTP request and returns an HTTP response, dispatching APM events
func (c *Client) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	// Use context from parameter
	req = req.WithContext(ctx)

	start := time.Now()
	reqURL := req.URL.String()
	method := req.Method

	// Get request size
	var requestSize int64
	if req.Body != nil && req.ContentLength > 0 {
		requestSize = req.ContentLength
	}

	resp, err := c.client.Do(req)
	duration := time.Since(start)

	if err != nil {
		c.dispatchRequestFailed(ctx, method, reqURL, err, duration)
		return nil, err
	}

	// Get response size
	var responseSize int64
	if resp.ContentLength > 0 {
		responseSize = resp.ContentLength
	}

	c.dispatchRequestSent(ctx, method, reqURL, resp.StatusCode, duration, requestSize, responseSize)
	return resp, nil
}

// Get performs a GET request
func (c *Client) Get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.resolveURL(url), nil)
	if err != nil {
		return nil, err
	}
	return c.Do(ctx, req)
}

// Post performs a POST request
func (c *Client) Post(ctx context.Context, url string, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.resolveURL(url), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	return c.Do(ctx, req)
}

// Put performs a PUT request
func (c *Client) Put(ctx context.Context, url string, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.resolveURL(url), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	return c.Do(ctx, req)
}

// Delete performs a DELETE request
func (c *Client) Delete(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.resolveURL(url), nil)
	if err != nil {
		return nil, err
	}
	return c.Do(ctx, req)
}

// Patch performs a PATCH request
func (c *Client) Patch(ctx context.Context, url string, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, c.resolveURL(url), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	return c.Do(ctx, req)
}

// Shutdown closes idle connections held by the underlying http.Client
// transport and honours the context deadline. Requests already in
// flight are not cancelled; callers should drive that through the
// request context.
func (c *Client) Shutdown(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.client != nil {
		if t, ok := c.client.Transport.(interface{ CloseIdleConnections() }); ok {
			t.CloseIdleConnections()
		} else {
			http.DefaultTransport.(*http.Transport).CloseIdleConnections()
		}
	}
	return nil
}

// SetEventDispatcher sets the function used to dispatch events.
func (c *Client) SetEventDispatcher(fn func(event interface{}) error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.eventDispatcher = fn
}

// dispatchEvent dispatches an event if a dispatcher is configured.
func (c *Client) dispatchEvent(event interface{}) {
	c.mu.RLock()
	fn := c.eventDispatcher
	c.mu.RUnlock()
	if fn != nil {
		fn(event)
	}
}

// resolveURL resolves the URL with the base URL if set
func (c *Client) resolveURL(url string) string {
	if c.baseURL != "" && len(url) > 0 && url[0] == '/' {
		return c.baseURL + url
	}
	return url
}
