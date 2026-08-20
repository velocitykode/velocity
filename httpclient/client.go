// Package httpclient provides an instrumented HTTP client for APM monitoring.
// It wraps the standard http.Client and dispatches events for outgoing
// requests.
//
// Defaults are secure: TLS 1.2 minimum, capped redirect chain (sensitive
// headers stripped on cross-origin hops), an SSRF dial guard that
// refuses connections to loopback, RFC1918, link-local, CGNAT, and
// cloud-metadata IPs (IPv4 and IPv6), the standard library's HTTP_PROXY
// environment hook is cleared so a hostile env value cannot route
// outbound traffic through an attacker-controlled CONNECT proxy, and
// per-stage transport timeouts are pinned (TLS handshake 10s, response
// header 30s, idle conn 90s, expect-continue 1s) so a slow-header
// upstream cannot starve the calling goroutine. Pair with
// [WithAllowedHosts] to whitelist specific internal services,
// [WithProxyAllowed] to honour HTTP(S)_PROXY env in trusted egress
// gateways, [WithResponseHeaderTimeout] / [WithTLSHandshakeTimeout] /
// [WithIdleConnTimeout] / [WithExpectContinueTimeout] to tune per-stage
// budgets, or [WithoutPrivateIPDeny] to disable the SSRF guard entirely
// for tests or trusted callers.
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

// ErrResponseTooLarge is returned from response body reads when the
// configured maximum response size (see [WithMaxResponseBytes]) is
// exceeded. Bound at read time, not at Content-Length inspection: a
// server lying about Content-Length cannot bypass the limit.
var ErrResponseTooLarge = errors.New("velocity/httpclient: response body exceeded max bytes")

// defaultMaxRedirects caps redirect chains. The stdlib default is 10;
// the framework explicitly enforces the same cap so WithMaxRedirects can
// tighten it.
const defaultMaxRedirects = 10

// defaultMaxResponseBytes is the default per-response read cap (32 MiB).
// Sized to comfortably hold typical webhook / API payloads while still
// preventing an attacker-controlled endpoint from OOM-ing the host.
const defaultMaxResponseBytes int64 = 32 << 20

// Granular transport timeouts. http.DefaultTransport.Clone() already
// supplies TLSHandshakeTimeout (10s), IdleConnTimeout (90s), and
// ExpectContinueTimeout (1s); these constants pin the same values so a
// caller-supplied transport (WithHTTPClient) that omits them still gets
// reasonable defaults applied by buildTransport. ResponseHeaderTimeout
// is *not* in the stdlib default, so the absence is what lets a server
// that opens a TCP connection and dribbles bytes (slowloris) starve the
// goroutine; pinning a default closes that window while still letting
// operators raise the cap for slow-rendering upstreams.
const (
	defaultTLSHandshakeTimeout   = 10 * time.Second
	defaultResponseHeaderTimeout = 30 * time.Second
	defaultIdleConnTimeout       = 90 * time.Second
	defaultExpectContinueTimeout = 1 * time.Second
)

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
	eventDispatcher func(ctx context.Context, event interface{}) error

	// Security options (configured via Option funcs).
	minTLSVersion    uint16
	maxRedirects     int
	denyPrivateIPs   bool
	proxyAllowed     bool                // opt-in to honour HTTP(S)_PROXY env when denyPrivateIPs is true
	allowedHosts     map[string]struct{} // eTLD+1 allowlist for private-IP deny
	resolver         *net.Resolver
	customTransport  bool  // set when WithHTTPClient supplies its own Transport
	maxResponseBytes int64 // <=0 disables the response body cap

	// directDial records (at construction, read-only afterwards) that the
	// transport is a guarded *http.Transport with no Proxy hook, so every
	// new connection (initial request and redirect hops alike) passes
	// through dialContextGuarded, which resolves, checks, and IP-pins the
	// upstream host. On that path the URL-host gate skips its per-request
	// DNS lookup, which would otherwise fire even on keep-alive reuse
	// where no dial happens. Proxy mode and custom non-*http.Transport
	// RoundTrippers keep the gate's lookup: there the dial guard never
	// sees the upstream host (or is not installed at all).
	directDial bool

	// Granular transport timeouts. nil means "use the framework default";
	// a non-nil pointer is honoured verbatim (including 0, which the
	// stdlib reads as "no timeout"). Pointer indirection is the cleanest
	// way to distinguish "operator did not set this" from "operator
	// chose 0 on purpose".
	tlsHandshakeTimeout   *time.Duration
	responseHeaderTimeout *time.Duration
	idleConnTimeout       *time.Duration
	expectContinueTimeout *time.Duration
}

// Option configures a Client
type Option func(*Client)

// WithHTTPClient sets a custom http.Client. When used, TLS/redirect/SSRF
// options applied after this call take effect on the caller's transport
// only if it is an *http.Transport, otherwise they are ignored.
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

// WithTLSHandshakeTimeout caps the duration of the TLS handshake on
// the underlying transport. The framework default is 10s (matching the
// stdlib's [http.DefaultTransport]). Pass 0 to disable the timeout
// entirely; pair only with trusted backends.
func WithTLSHandshakeTimeout(d time.Duration) Option {
	return func(c *Client) {
		c.tlsHandshakeTimeout = &d
	}
}

// WithResponseHeaderTimeout caps the time the transport will wait for
// the upstream's response status line + headers after writing the
// request. Closes the slowloris window where a server opens the TCP
// connection and dribbles header bytes to starve the calling goroutine.
//
// The framework default is 30s. The stdlib does *not* set this field;
// the framework supplies a value by default so calls cannot block on
// the response indefinitely under the only-WithTimeout configuration.
// Pass 0 to disable the per-stage timeout (still bounded by
// [Client.Timeout] / request context if those are set).
func WithResponseHeaderTimeout(d time.Duration) Option {
	return func(c *Client) {
		c.responseHeaderTimeout = &d
	}
}

// WithIdleConnTimeout sets the maximum amount of time an idle (keep-alive)
// connection will remain idle in the transport's pool before closing.
// The framework default is 90s (matching the stdlib's
// [http.DefaultTransport]). Pass 0 to disable (idle conns never expire).
func WithIdleConnTimeout(d time.Duration) Option {
	return func(c *Client) {
		c.idleConnTimeout = &d
	}
}

// WithExpectContinueTimeout sets the timeout when sending a request
// with an "Expect: 100-continue" header. The framework default is 1s
// (matching the stdlib's [http.DefaultTransport]). Pass 0 to send the
// body immediately without waiting for a 100 Continue.
func WithExpectContinueTimeout(d time.Duration) Option {
	return func(c *Client) {
		c.expectContinueTimeout = &d
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
//
// The deny is on by default; this option is kept for callers that toggle
// it explicitly or that previously disabled it via [WithoutPrivateIPDeny].
func WithPrivateIPDeny() Option {
	return func(c *Client) {
		c.denyPrivateIPs = true
	}
}

// WithoutPrivateIPDeny disables the default SSRF dial guard, allowing
// connections to loopback, RFC1918, link-local, CGNAT, and cloud-metadata
// addresses. Use only for tests or for callers that intentionally reach
// internal services and accept the risk. Prefer [WithAllowedHosts] for a
// targeted exception.
func WithoutPrivateIPDeny() Option {
	return func(c *Client) {
		c.denyPrivateIPs = false
	}
}

// WithMaxResponseBytes caps how many bytes will be read from any
// response body. Reads beyond the cap return [ErrResponseTooLarge].
// The default is 32 MiB; pass 0 or a negative value to disable the cap
// entirely (use only for trusted endpoints streaming bulk data).
//
// The cap is enforced on the read stream itself, not on the
// Content-Length header, so a server that lies about Content-Length
// cannot bypass it.
func WithMaxResponseBytes(n int64) Option {
	return func(c *Client) {
		c.maxResponseBytes = n
	}
}

// WithProxyAllowed opts back into honouring HTTP_PROXY / HTTPS_PROXY /
// NO_PROXY environment variables when the SSRF dial guard is on. Without
// this option, [New] clears the default transport's
// [http.ProxyFromEnvironment] hook so an attacker who can influence the
// process environment cannot tunnel the client through a hostile proxy
// (which would defeat the dial-time private-IP guard by terminating the
// TCP connection at a public proxy IP and CONNECT-ing onwards to a
// metadata service inside the trust boundary).
//
// Use this option for clients that run inside environments where a
// trusted proxy is configured via env (CI runners, corporate egress
// gateways) and the proxy itself is trusted.
//
// Callers that supply a transport via [WithHTTPClient] already control
// the Proxy field explicitly; this option is therefore a no-op for that
// path. The deny is also a no-op when [WithoutPrivateIPDeny] has been
// applied (env proxies pass through unmodified in that mode).
func WithProxyAllowed() Option {
	return func(c *Client) {
		c.proxyAllowed = true
	}
}

// WithAllowedHosts whitelists specific eTLD+1 hosts from the private-IP
// deny list, useful when you legitimately need to reach an internal
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
// cross-host redirects, and the SSRF dial guard ([WithPrivateIPDeny])
// enabled. Use [WithAllowedHosts] to whitelist specific internal hosts,
// or [WithoutPrivateIPDeny] to disable the guard entirely (tests only).
func New(opts ...Option) *Client {
	c := &Client{
		client:           &http.Client{Timeout: 30 * time.Second},
		minTLSVersion:    tls.VersionTLS12,
		maxRedirects:     defaultMaxRedirects,
		denyPrivateIPs:   true,
		resolver:         net.DefaultResolver,
		maxResponseBytes: defaultMaxResponseBytes,
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

	// Direct-dial fast path: a guarded *http.Transport with no Proxy hook
	// dials the upstream host itself, so dialContextGuarded already
	// resolves, checks, and pins every new connection. The URL-host gate
	// can then skip its blocking DNS lookup (cheap literal-IP and
	// allowlist checks still run). Any Proxy hook, even a conditional one
	// like http.ProxyFromEnvironment, keeps the lookup: the dial target
	// may be the proxy, not the upstream. Caller-supplied transports
	// (WithHTTPClient) always keep it, even when they currently look
	// direct-dial: the caller retains the *http.Client and can mutate
	// Proxy, TLS dial hooks, or RegisterProtocol alternate round
	// trippers after construction, all of which route around the
	// guarded DialContext while this flag would go stale. Only the
	// framework-built transport is sealed: no caller reference escapes,
	// no TLS dial hooks, no alt protocols, Proxy cleared unless
	// WithProxyAllowed. The hook checks below are belt-and-braces
	// re-assertions of that invariant.
	if t, ok := c.client.Transport.(*http.Transport); ok && !c.customTransport {
		c.directDial = c.denyPrivateIPs && t.Proxy == nil &&
			t.DialTLSContext == nil && t.DialTLS == nil
	}

	return c
}

// buildTransport returns a transport with TLS minimum and SSRF-guard
// DialContext applied. If base is non-nil, its pooling/idle-timeout
// settings are preserved.
//
// When base is nil (the default-construction path), the cloned
// [http.DefaultTransport] carries [http.ProxyFromEnvironment]. If the
// SSRF dial guard is enabled and the caller has not opted in via
// [WithProxyAllowed], the proxy hook is cleared: an attacker who can
// influence HTTP_PROXY / HTTPS_PROXY in the process environment could
// otherwise tunnel outbound requests through a hostile proxy that
// CONNECT-relays to a metadata IP, defeating the dial-time guard which
// only sees the (public) proxy address.
//
// Granular per-stage timeouts (TLSHandshakeTimeout, ResponseHeaderTimeout,
// IdleConnTimeout, ExpectContinueTimeout) are pinned to framework
// defaults on the default-construction path so the only-WithTimeout
// configuration cannot block indefinitely on any single stage (the
// stdlib's DefaultTransport omits ResponseHeaderTimeout, which is the
// classic slowloris vector). With* options override each default
// individually; passing 0 disables that stage's timeout.
//
// Caller-supplied transports (base != nil) are left untouched: the
// caller has set Proxy and timeouts explicitly and [WithHTTPClient]
// documents that transport-level fields are theirs to own.
func (c *Client) buildTransport(base *http.Transport) *http.Transport {
	t := base
	clonedFromDefault := false
	if t == nil {
		t = http.DefaultTransport.(*http.Transport).Clone()
		clonedFromDefault = true
	}
	if t.TLSClientConfig == nil {
		t.TLSClientConfig = &tls.Config{}
	}
	if c.minTLSVersion != 0 && t.TLSClientConfig.MinVersion < c.minTLSVersion {
		t.TLSClientConfig.MinVersion = c.minTLSVersion
	}
	if clonedFromDefault && c.denyPrivateIPs && !c.proxyAllowed {
		t.Proxy = nil
	}
	if c.denyPrivateIPs {
		t.DialContext = c.dialContextGuarded(t.DialContext)
	}
	c.applyTransportTimeouts(t, clonedFromDefault)
	return t
}

// applyTransportTimeouts pins per-stage timeouts on the transport.
//
// On the default-construction path (clonedFromDefault=true) the
// framework-default value is applied whenever the operator has not
// passed a With* override; ResponseHeaderTimeout is forced because the
// stdlib default omits it. On the caller-supplied path we only touch
// the field when the operator has explicitly asked via a With* option,
// which keeps WithHTTPClient honouring its "your transport, your
// fields" contract.
func (c *Client) applyTransportTimeouts(t *http.Transport, clonedFromDefault bool) {
	apply := func(field *time.Duration, override *time.Duration, def time.Duration) {
		switch {
		case override != nil:
			*field = *override
		case clonedFromDefault:
			*field = def
		}
	}
	apply(&t.TLSHandshakeTimeout, c.tlsHandshakeTimeout, defaultTLSHandshakeTimeout)
	apply(&t.ResponseHeaderTimeout, c.responseHeaderTimeout, defaultResponseHeaderTimeout)
	apply(&t.IdleConnTimeout, c.idleConnTimeout, defaultIdleConnTimeout)
	apply(&t.ExpectContinueTimeout, c.expectContinueTimeout, defaultExpectContinueTimeout)
}

// hostCheck is the result of evaluating a hostname against the SSRF
// allow / deny rules. Exactly one of (allowed, pinnedIP) is meaningful
// for the caller:
//
//   - allowed=true, pinnedIP=="": host was on the allowlist, dial as-is.
//   - allowed=true, pinnedIP!="": host resolved to a public address,
//     dial the pinned IP to defeat DNS rebinding between this check and
//     the real connect.
//
// The dial guard uses pinnedIP; the URL-host gate ignores it.
type hostCheck struct {
	pinnedIP string
}

// evaluateHost reports whether the given hostname is permitted under
// the client's deny / allowlist rules. The same code path is used by
// the URL-host gate (so proxy-mode is covered) and by the dial-time
// guard (so direct-dial still gets the pinned-IP TOCTOU protection).
//
// resolve controls whether a non-literal hostname is DNS-resolved and
// its addresses checked. The dial-time guard always resolves (it needs
// the pinned IP anyway); the URL-host gate skips resolution on the
// direct-dial path, where the dial guard is known to cover every new
// connection, and resolves in proxy / custom-RoundTripper mode where
// the gate is the only check that sees the upstream host.
//
// When denyPrivateIPs is false the function returns ok with no pinned
// IP, leaving the caller's existing dial behaviour untouched.
func (c *Client) evaluateHost(ctx context.Context, host string, resolve bool) (hostCheck, error) {
	if !c.denyPrivateIPs {
		return hostCheck{}, nil
	}
	// Allowlist short-circuits the deny for a specific eTLD+1.
	if _, allowed := c.allowedHosts[strings.ToLower(neturl.ETLDPlusOne(host))]; allowed {
		return hostCheck{}, nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if neturl.IsPrivateOrInternal(ip) {
			return hostCheck{}, fmt.Errorf("velocity/httpclient: refusing to reach %s: %w", host, errPrivateIP)
		}
		return hostCheck{}, nil
	}
	if !resolve {
		return hostCheck{}, nil
	}
	addrs, err := c.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return hostCheck{}, fmt.Errorf("velocity/httpclient: resolve %s: %w", host, err)
	}
	if len(addrs) == 0 {
		return hostCheck{}, fmt.Errorf("velocity/httpclient: resolve %s: no addresses", host)
	}
	for _, a := range addrs {
		if neturl.IsPrivateOrInternal(a.IP) {
			return hostCheck{}, fmt.Errorf("velocity/httpclient: refusing to reach %s (resolves to %s): %w", host, a.IP, errPrivateIP)
		}
	}
	return hostCheck{pinnedIP: addrs[0].IP.String()}, nil
}

// dialContextGuarded wraps dial so every outbound TCP connection is
// resolved ahead of time and rejected if any resolved address falls in a
// disallowed private range. The resolved IP is pinned into the dial to
// prevent TOCTOU / DNS-rebinding between this check and the real dial.
//
// This is the enforcement point for every path that opens a connection:
// on direct dials it is where hostnames are resolved and checked (the
// URL-host gate in [Client.Do] and [Client.checkRedirect] runs only the
// cheap non-DNS checks there), and it pins the resolved IP against
// TOCTOU. In proxy mode the dial target is the proxy, not the upstream,
// so the URL-host gate keeps its full resolving check as the first line
// of defence.
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
		hc, err := c.evaluateHost(ctx, host, true)
		if err != nil {
			return nil, err
		}
		if hc.pinnedIP != "" {
			// Pin first resolved address to prevent re-resolution between check and dial.
			return inner(ctx, network, net.JoinHostPort(hc.pinnedIP, port))
		}
		return inner(ctx, network, addr)
	}
}

// checkRedirect is the stdlib CheckRedirect callback. It caps the chain
// length, re-runs the URL-host SSRF gate on every hop (so a public
// origin cannot 302 the client to an internal address), and strips
// sensitive headers when the next host (eTLD+1) differs from the
// original request host. Context from the first request is preserved
// automatically by net/http.
func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if c.maxRedirects <= 0 {
		return http.ErrUseLastResponse
	}
	if len(via) >= c.maxRedirects {
		return fmt.Errorf("velocity/httpclient: stopped after %d redirects", len(via))
	}
	if err := c.assertURLAllowed(req.Context(), req.URL); err != nil {
		return err
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

// assertURLAllowed runs the SSRF host check against the URL of a
// pending request. Called for the initial request and for every
// redirect hop so the check fires regardless of whether the underlying
// transport ends up dialling the upstream directly or via a proxy.
//
// On the direct-dial path (see the directDial field) only the cheap
// checks run here (allowlist, literal-IP); the blocking DNS lookup is
// left to dialContextGuarded, which performs it exactly once per new
// connection instead of once per request. Blocked hosts caught there
// surface wrapped in *url.Error by net/http; errors.Is / errors.As
// still match through the wrap.
func (c *Client) assertURLAllowed(ctx context.Context, u *url.URL) error {
	if !c.denyPrivateIPs || u == nil {
		return nil
	}
	host := u.Hostname()
	if host == "" {
		return nil
	}
	if _, err := c.evaluateHost(ctx, host, !c.directDial); err != nil {
		return err
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

	// URL-host SSRF gate runs before the transport so the check covers
	// proxy mode (where the dial target is the proxy, not the upstream).
	// On the direct-dial path it runs only the cheap checks; the DNS
	// resolution happens once per new connection in dialContextGuarded.
	// Bypasses cleanly when denyPrivateIPs is off.
	if err := c.assertURLAllowed(ctx, req.URL); err != nil {
		c.dispatchRequestFailed(ctx, method, reqURL, err, time.Since(start))
		return nil, err
	}

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

	// Cap the read stream so a hostile or misbehaving server cannot OOM
	// the host. The wrapper preserves the underlying body's Close so
	// connection reuse and idle pooling continue to work.
	if c.maxResponseBytes > 0 {
		resp.Body = &limitedBody{
			r:      io.LimitReader(resp.Body, c.maxResponseBytes+1),
			closer: resp.Body,
			max:    c.maxResponseBytes,
		}
	}

	c.dispatchRequestSent(ctx, method, reqURL, resp.StatusCode, duration, requestSize, responseSize)
	return resp, nil
}

// limitedBody enforces a maximum read count over an HTTP response body
// without preventing the caller from closing it (which is what allows
// the transport to reuse the connection). Once max bytes have been
// delivered to the caller, the next Read returns [ErrResponseTooLarge].
//
// The underlying reader is an io.LimitReader sized to max+1 so the
// wrapper can distinguish "exactly at the cap" (still fine) from
// "over the cap" (refuse).
type limitedBody struct {
	r      io.Reader
	closer io.Closer
	max    int64
	read   int64
	tipped bool
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.tipped {
		return 0, ErrResponseTooLarge
	}
	n, err := b.r.Read(p)
	b.read += int64(n)
	if b.read > b.max {
		// Surface the cap on the same call so callers using io.ReadAll
		// observe the failure immediately instead of after one extra
		// zero-byte Read.
		over := b.read - b.max
		// Trim the slice so the caller only sees bytes up to the cap.
		if int64(n) >= over {
			n -= int(over)
		} else {
			n = 0
		}
		b.read = b.max
		b.tipped = true
		return n, ErrResponseTooLarge
	}
	return n, err
}

func (b *limitedBody) Close() error {
	if b.closer == nil {
		return nil
	}
	return b.closer.Close()
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
func (c *Client) SetEventDispatcher(fn func(ctx context.Context, event interface{}) error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.eventDispatcher = fn
}

// dispatchEvent dispatches an event if a dispatcher is configured. The
// caller-supplied ctx is propagated so listeners observe request-scoped
// values.
func (c *Client) dispatchEvent(ctx context.Context, event interface{}) {
	c.mu.RLock()
	fn := c.eventDispatcher
	c.mu.RUnlock()
	if fn != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		fn(ctx, event)
	}
}

// resolveURL resolves the URL with the base URL if set
func (c *Client) resolveURL(url string) string {
	if c.baseURL != "" && len(url) > 0 && url[0] == '/' {
		return c.baseURL + url
	}
	return url
}
