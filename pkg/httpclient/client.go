// Package httpclient provides an instrumented HTTP client for APM monitoring.
// It wraps the standard http.Client and dispatches events for outgoing requests.
package httpclient

import (
	"context"
	"io"
	"net/http"
	"time"
)

// Client is an instrumented HTTP client that dispatches events for APM monitoring
type Client struct {
	client  *http.Client
	baseURL string
}

// Option configures a Client
type Option func(*Client)

// WithHTTPClient sets a custom http.Client
func WithHTTPClient(client *http.Client) Option {
	return func(c *Client) {
		c.client = client
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

// New creates a new instrumented HTTP client
func New(opts ...Option) *Client {
	c := &Client{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// Do sends an HTTP request and returns an HTTP response, dispatching APM events
func (c *Client) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	// Use context from parameter
	req = req.WithContext(ctx)

	start := time.Now()
	url := req.URL.String()
	method := req.Method

	// Get request size
	var requestSize int64
	if req.Body != nil && req.ContentLength > 0 {
		requestSize = req.ContentLength
	}

	resp, err := c.client.Do(req)
	duration := time.Since(start)

	if err != nil {
		dispatchRequestFailed(ctx, method, url, err, duration)
		return nil, err
	}

	// Get response size
	var responseSize int64
	if resp.ContentLength > 0 {
		responseSize = resp.ContentLength
	}

	dispatchRequestSent(ctx, method, url, resp.StatusCode, duration, requestSize, responseSize)
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

// resolveURL resolves the URL with the base URL if set
func (c *Client) resolveURL(url string) string {
	if c.baseURL != "" && len(url) > 0 && url[0] == '/' {
		return c.baseURL + url
	}
	return url
}

// Default is the default instrumented HTTP client
var Default = New()

// Get performs a GET request using the default client
func Get(ctx context.Context, url string) (*http.Response, error) {
	return Default.Get(ctx, url)
}

// Post performs a POST request using the default client
func Post(ctx context.Context, url string, contentType string, body io.Reader) (*http.Response, error) {
	return Default.Post(ctx, url, contentType, body)
}

// Put performs a PUT request using the default client
func Put(ctx context.Context, url string, contentType string, body io.Reader) (*http.Response, error) {
	return Default.Put(ctx, url, contentType, body)
}

// Delete performs a DELETE request using the default client
func Delete(ctx context.Context, url string) (*http.Response, error) {
	return Default.Delete(ctx, url)
}

// Patch performs a PATCH request using the default client
func Patch(ctx context.Context, url string, contentType string, body io.Reader) (*http.Response, error) {
	return Default.Patch(ctx, url, contentType, body)
}
