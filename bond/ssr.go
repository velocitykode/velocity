package bond

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
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

	mu              sync.RWMutex
	eventDispatcher func(event interface{}) error
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

// NewHTTPGateway constructs a gateway that POSTs page JSON to url.
// An empty url falls back to DefaultSSRURL. If url has no path, /render
// is appended to preserve the conventional Inertia SSR endpoint (users
// can override with a full URL for dev endpoints like /__inertia_ssr).
func NewHTTPGateway(url string) *HTTPGateway {
	if url == "" {
		url = DefaultSSRURL
	}
	url = strings.TrimRight(url, "/")
	if !strings.Contains(strings.TrimPrefix(strings.TrimPrefix(url, "http://"), "https://"), "/") {
		url += "/render"
	}
	return &HTTPGateway{
		URL:     url,
		Timeout: defaultSSRTimeout,
		Client: &http.Client{
			Timeout: defaultSSRTimeout,
		},
	}
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
		return g.handleFailure(page, payload, fmt.Errorf("%s", payload.Error))
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
