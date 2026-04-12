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
// to signal "fall back to CSR" — only return a non-nil error for
// programmer errors that callers must handle.
type SSRGateway interface {
	Dispatch(ctx context.Context, page Page) (*SSRResponse, error)
}

// HTTPGateway dispatches pages to a Node SSR server over HTTP.
// It mirrors the design of inertia-laravel's HttpGateway:
//   - Graceful fallback to CSR on any transport/parse failure
//   - Configurable URL (the default 127.0.0.1:13714 matches the
//     standard Inertia SSR port used by inertia-laravel and gonertia)
//   - Configurable timeout — defaults to 3s so a slow SSR server
//     cannot stall page rendering
type HTTPGateway struct {
	URL     string
	Timeout time.Duration
	Client  *http.Client
	// OnFailure is called whenever a dispatch fails before falling back
	// to CSR. Callers can use it to emit metrics or log. Never called
	// when SSR is disabled or the page is excluded.
	OnFailure func(page Page, err error)
	// Except skips SSR for any request whose page URL starts with one
	// of these prefixes. Matches Laravel's ExcludesSsrPaths concern.
	Except []string
}

// DefaultSSRURL is the conventional Inertia SSR server address used by
// inertia-laravel and gonertia. Production SSR bundles (`node ssr.js`)
// listen here by default. In dev, point at Vite's /__inertia_ssr hot
// endpoint instead (e.g. http://127.0.0.1:5173/__inertia_ssr).
const DefaultSSRURL = "http://127.0.0.1:13714/render"

const defaultSSRTimeout = 3 * time.Second

// NewHTTPGateway constructs a gateway that POSTs page JSON to url.
// An empty url falls back to DefaultSSRURL. If url has no path, /render
// is appended to preserve the Laravel/gonertia default (users can
// override with a full URL for dev endpoints like /__inertia_ssr).
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

// Dispatch POSTs the page JSON to the SSR server and returns the
// rendered response. Returns (nil, nil) on any failure after invoking
// OnFailure — callers must treat nil as "render CSR".
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
		g.notifyFailure(page, err)
		return nil, nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := g.Client
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		g.notifyFailure(page, err)
		return nil, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		g.notifyFailure(page, fmt.Errorf("ssr server returned %d", resp.StatusCode))
		return nil, nil
	}

	// Cap at 10 MiB. A pre-rendered page that exceeds this is almost
	// certainly an error or a misbehaving SSR server — CSR fallback
	// is safer than ballooning memory.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		g.notifyFailure(page, err)
		return nil, nil
	}

	var out SSRResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		g.notifyFailure(page, err)
		return nil, nil
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

	// Health endpoint is conventionally at /health on the SSR server root.
	// Derive it from the base host (strip any path on URL).
	base := g.URL
	if i := strings.Index(strings.TrimPrefix(strings.TrimPrefix(base, "http://"), "https://"), "/"); i >= 0 {
		scheme := ""
		if strings.HasPrefix(base, "https://") {
			scheme = "https://"
			base = strings.TrimPrefix(base, "https://")
		} else {
			scheme = "http://"
			base = strings.TrimPrefix(base, "http://")
		}
		base = scheme + base[:i]
	}

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

func (g *HTTPGateway) notifyFailure(page Page, err error) {
	if g.OnFailure != nil {
		g.OnFailure(page, err)
	}
}
