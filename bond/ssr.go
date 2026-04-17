package bond

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/velocitykode/velocity/internal/neturl"
)

// SSRResponse is the payload returned by the Node SSR server after
// rendering a page. head is a list of <head> tags (title, meta, link)
// and body is the inner HTML to embed inside the Inertia container.
type SSRResponse struct {
	Head []string `json:"head"`
	Body string   `json:"body"`
}

// SSRGateway dispatches an Inertia Page to a rendering service and
// returns server-rendered HTML. Implementations should return (nil, nil)
// to signal "fall back to CSR". A non-nil error means the caller should
// treat the failure as fatal (used for ThrowOnError mode in tests).
type SSRGateway interface {
	Dispatch(ctx context.Context, page Page) (*SSRResponse, error)
}

// HTTPGateway dispatches pages to a Node SSR server over HTTP.
// Implements the standard Inertia HTTP SSR gateway contract:
//   - Graceful fallback to CSR on any transport/parse failure
//   - Rich error payload parsing (type, hint, stack, sourceLocation,
//     browserApi) surfaced as SSRRenderFailed events
//   - Configurable URL — defaults to 127.0.0.1:13714/render, the
//     conventional Inertia SSR address. Dev deployments should point
//     at Vite's /__inertia_ssr hot endpoint instead
//   - ThrowOnError for E2E tests that need failures to bubble up
//   - SSRF protection: the target URL is validated against a private-IP
//     deny list unless constructed with WithAllowPrivate(true). Responses
//     are also capped at 10 MiB to avoid unbounded memory use if the SSR
//     server misbehaves or is attacker-controlled.
type HTTPGateway struct {
	URL     string
	Timeout time.Duration
	Client  *http.Client

	// Except skips SSR for any request whose page URL starts with one
	// of these prefixes. Equivalent to the Inertia ExcludesSsrPaths concern.
	Except []string

	// ThrowOnError makes Dispatch return the transport or render error
	// instead of swallowing it for CSR fallback. Equivalent to the
	// Inertia SSR `throw_on_error` config flag.
	ThrowOnError bool

	// allowPrivate disables the private-IP validation that would
	// otherwise refuse targets like 127.0.0.1. Defaults to true because
	// the typical production deployment runs the SSR server on the same
	// host, but can be tightened via WithAllowPrivate(false).
	allowPrivate bool

	mu              sync.RWMutex
	eventDispatcher func(event interface{}) error
}

// GatewayOption configures an HTTPGateway at construction time.
type GatewayOption func(*HTTPGateway)

// WithAllowPrivate controls whether the gateway accepts targets that
// resolve to private, loopback, link-local, or cloud-metadata ranges.
// Defaults to true because the conventional Inertia SSR deployment runs
// the Node server on the same host as the Go app. Set to false when the
// URL is expected to be public or caller-controlled.
func WithAllowPrivate(allow bool) GatewayOption {
	return func(g *HTTPGateway) {
		g.allowPrivate = allow
	}
}

// DefaultSSRURL is the conventional Inertia SSR server address.
// Production SSR bundles (`node ssr.js`) listen here by default. In
// dev, point at Vite's /__inertia_ssr hot endpoint instead
// (e.g. http://127.0.0.1:5173/__inertia_ssr).
const DefaultSSRURL = "http://127.0.0.1:13714/render"

const defaultSSRTimeout = 3 * time.Second

// ssrServerError is the structured error payload an inertia-aware SSR
// server returns in the response body on failure. Unknown fields are
// tolerated — the SSR server controls this contract.
type ssrServerError struct {
	Error          string `json:"error"`
	Type           string `json:"type"`
	Hint           string `json:"hint"`
	BrowserAPI     string `json:"browserApi"`
	Stack          string `json:"stack"`
	SourceLocation string `json:"sourceLocation"`
}

// NewHTTPGateway constructs a gateway that POSTs page JSON to rawURL.
// An empty rawURL falls back to DefaultSSRURL. If rawURL has no path,
// /render is appended to preserve the conventional Inertia SSR endpoint
// (users can override with a full URL for dev endpoints like
// /__inertia_ssr).
//
// By default the target URL may resolve to private/loopback ranges —
// the typical Inertia deployment runs the Node SSR server on the same
// host. Pass WithAllowPrivate(false) to forbid private targets (useful
// when SSR is hosted externally and the URL should never point inside
// the VPC).
func NewHTTPGateway(rawURL string, opts ...GatewayOption) *HTTPGateway {
	if rawURL == "" {
		rawURL = DefaultSSRURL
	}
	rawURL = strings.TrimRight(rawURL, "/")
	if !strings.Contains(strings.TrimPrefix(strings.TrimPrefix(rawURL, "http://"), "https://"), "/") {
		rawURL += "/render"
	}
	g := &HTTPGateway{
		URL:     rawURL,
		Timeout: defaultSSRTimeout,
		Client: &http.Client{
			Timeout: defaultSSRTimeout,
		},
		allowPrivate: true,
	}
	for _, opt := range opts {
		opt(g)
	}

	// Validate target host unless private addresses are explicitly allowed.
	// The SSR endpoint is config-driven, not user-driven, so a validation
	// failure is a configuration bug — we zero the URL so Dispatch skips
	// SSR (CSR fallback), and the misconfiguration surfaces on the first
	// render via the SSRRenderFailed event stream or boot-time logs.
	if !g.allowPrivate {
		if err := validateSSRTarget(rawURL); err != nil {
			g.URL = ""
		}
	}
	return g
}

// validateSSRTarget parses target and rejects hosts that resolve to
// private/internal ranges. Uses the shared neturl guard so the policy
// stays consistent across httpclient and notification channels.
func validateSSRTarget(target string) error {
	u, err := url.Parse(target)
	if err != nil {
		return fmt.Errorf("bond: parse ssr url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("bond: ssr url must be http or https")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := neturl.ValidateURLHost(ctx, nil, target); err != nil {
		return fmt.Errorf("bond: ssr target rejected: %w", err)
	}
	return nil
}

// SetEventDispatcher wires the framework event bus so dispatch failures
// flow out as SSRRenderFailed events. Safe to call from any goroutine.
func (g *HTTPGateway) SetEventDispatcher(fn func(event interface{}) error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.eventDispatcher = fn
}

// Dispatch POSTs the page JSON to the SSR server and returns the
// rendered response. On any failure it emits an SSRRenderFailed event
// and (unless ThrowOnError is set) returns (nil, nil) so renderHTML
// falls back to CSR. When ThrowOnError is true the real error is
// returned so callers can surface it — useful for E2E tests.
func (g *HTTPGateway) Dispatch(ctx context.Context, page Page) (*SSRResponse, error) {
	if g == nil || g.URL == "" {
		return nil, nil
	}
	if g.excluded(page.URL) {
		return nil, nil
	}

	body, err := json.Marshal(page)
	if err != nil {
		return nil, fmt.Errorf("bond: marshal page for SSR: %w", err)
	}

	timeout := g.Timeout
	if timeout <= 0 {
		timeout = defaultSSRTimeout
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, g.URL, bytes.NewReader(body))
	if err != nil {
		return g.handleFailure(page, ssrServerError{
			Error: err.Error(),
			Type:  string(SSRErrorConnection),
		}, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := g.Client
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return g.handleFailure(page, ssrServerError{
			Error: err.Error(),
			Type:  string(SSRErrorConnection),
		}, err)
	}
	defer resp.Body.Close()

	// Cap at 10 MiB. A pre-rendered page that exceeds this is almost
	// certainly an error or a misbehaving SSR server — CSR fallback
	// is safer than ballooning memory.
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if readErr != nil {
		return g.handleFailure(page, ssrServerError{
			Error: readErr.Error(),
			Type:  string(SSRErrorConnection),
		}, readErr)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// SSR server returned an error. It may have included a
		// structured JSON payload describing what went wrong.
		payload := ssrServerError{
			Error: fmt.Sprintf("ssr server returned %d", resp.StatusCode),
			Type:  string(SSRErrorUnknown),
		}
		var parsed ssrServerError
		if json.Unmarshal(raw, &parsed) == nil && parsed.Error != "" {
			payload = parsed
		}
		return g.handleFailure(page, payload, fmt.Errorf("bond: ssr server error: %w", errors.New(payload.Error)))
	}

	var out SSRResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return g.handleFailure(page, ssrServerError{
			Error: err.Error(),
			Type:  string(SSRErrorRender),
		}, err)
	}

	return &out, nil
}

// IsHealthy pings the SSR server's /health endpoint. Used by diagnostics;
// not consulted during normal rendering (which relies on Dispatch's
// graceful fallback). Returns an error only for programmer mistakes —
// an unreachable server returns (false, nil).
func (g *HTTPGateway) IsHealthy(ctx context.Context) (bool, error) {
	if g == nil || g.URL == "" {
		return false, errors.New("bond: ssr gateway url not configured")
	}

	reqCtx, cancel := context.WithTimeout(ctx, g.Timeout)
	defer cancel()

	base := healthBaseURL(g.URL)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, base+"/health", nil)
	if err != nil {
		return false, err
	}

	client := g.Client
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return false, nil
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300, nil
}

// healthBaseURL strips the render path from the gateway URL so the
// health probe can hit /health on the same origin.
func healthBaseURL(u string) string {
	scheme := "http://"
	rest := u
	switch {
	case strings.HasPrefix(rest, "https://"):
		scheme = "https://"
		rest = strings.TrimPrefix(rest, "https://")
	case strings.HasPrefix(rest, "http://"):
		rest = strings.TrimPrefix(rest, "http://")
	}
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	return scheme + rest
}

func (g *HTTPGateway) excluded(pageURL string) bool {
	if len(g.Except) == 0 || pageURL == "" {
		return false
	}
	for _, prefix := range g.Except {
		if prefix == "" {
			continue
		}
		if strings.HasPrefix(pageURL, prefix) {
			return true
		}
	}
	return false
}

// handleFailure emits the SSRRenderFailed event and returns either
// (nil, nil) for graceful CSR fallback, or (nil, err) when ThrowOnError
// is set so callers can surface the failure.
func (g *HTTPGateway) handleFailure(page Page, payload ssrServerError, err error) (*SSRResponse, error) {
	g.mu.RLock()
	dispatch := g.eventDispatcher
	g.mu.RUnlock()

	if dispatch != nil {
		_ = dispatch(SSRRenderFailed{
			Component:      page.Component,
			URL:            page.URL,
			Error:          payload.Error,
			Type:           ParseSSRErrorType(payload.Type),
			Hint:           payload.Hint,
			BrowserAPI:     payload.BrowserAPI,
			Stack:          payload.Stack,
			SourceLocation: payload.SourceLocation,
		})
	}

	if g.ThrowOnError {
		if err == nil {
			err = errors.New(payload.Error)
		}
		return nil, err
	}
	return nil, nil
}
