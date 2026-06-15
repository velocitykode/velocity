// Package testing provides an outbound-HTTP fake for the httpclient package.
//
// The fake is a custom [http.RoundTripper] that stubs responses by
// method/URL matcher and records every request it is asked to send. It is
// injected via [httpclient.WithHTTPClient], which fully bypasses the network,
// TLS, and transport timeouts:
//
//	func TestFetchUser(t *testing.T) {
//	    fake := httpclienttest.New()
//	    fake.Stub(
//	        httpclienttest.MatchMethodAndURL(http.MethodGet, "/users/1"),
//	        httpclienttest.NewResponse(http.StatusOK, []byte(`{"id":1}`)),
//	    )
//
//	    client := fake.Client(httpclient.WithBaseURL("https://api.example.com"))
//	    resp, err := client.Get(context.Background(), "/users/1")
//	    // ...
//
//	    fake.AssertSent(t, httpclienttest.MatchURL("/users/1"))
//	}
//
// SSRF note: the client's SSRF host gate ([httpclient.WithPrivateIPDeny], on by
// default) runs BEFORE the transport, so it is independent of the fake. A stub
// for a loopback or private URL is still rejected by the gate before the fake
// ever sees the request. To exercise such URLs, also opt the client out via
// [httpclient.WithoutPrivateIPDeny]:
//
//	client := fake.Client(httpclient.WithoutPrivateIPDeny())
package testing

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/httpclient"
)

// maxFakeBodyBytes caps the request/response bodies the fake reads into memory
// when recording requests and cloning stub responses. Test payloads are small;
// the bound is defense in depth so a pathological body cannot exhaust memory.
const maxFakeBodyBytes = 10 << 20 // 10 MiB

// Matcher reports whether a request should be served by a given stub.
type Matcher func(*http.Request) bool

// MatchMethod matches any request whose method equals m.
func MatchMethod(m string) Matcher {
	return func(req *http.Request) bool {
		return req.Method == m
	}
}

// MatchURL matches any request whose full URL contains substr.
func MatchURL(substr string) Matcher {
	return func(req *http.Request) bool {
		if req.URL == nil {
			return false
		}
		return strings.Contains(req.URL.String(), substr)
	}
}

// MatchMethodAndURL matches requests whose method equals m and whose full URL
// contains substr.
func MatchMethodAndURL(m, substr string) Matcher {
	method := MatchMethod(m)
	url := MatchURL(substr)
	return func(req *http.Request) bool {
		return method(req) && url(req)
	}
}

type stub struct {
	match Matcher
	resp  *http.Response
}

// Fake is an outbound-HTTP fake. It implements [http.RoundTripper], recording
// every request and serving the first matching stubbed response. The zero
// value is not ready for use; obtain one via [New].
//
// A Fake is safe for concurrent use.
type Fake struct {
	mu       sync.Mutex
	requests []*http.Request
	stubs    []stub
}

// New returns a ready-to-use Fake with no stubs.
func New() *Fake {
	return &Fake{}
}

// Stub registers a stubbed response served to the first request that match
// accepts. Stubs are matched in registration order. Stub is chainable.
func (f *Fake) Stub(match Matcher, resp *http.Response) *Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stubs = append(f.stubs, stub{match: match, resp: resp})
	return f
}

// RoundTrip records a copy of req and returns a fresh clone of the first
// matching stub's response. It returns an error if no stub matches.
func (f *Fake) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.requests = append(f.requests, recordRequest(req))

	for _, s := range f.stubs {
		if s.match(req) {
			return cloneResponse(s.resp), nil
		}
	}
	return nil, fmt.Errorf("httpclienttest: no stubbed response for %s %s", req.Method, req.URL)
}

// Client returns an *httpclient.Client wired to this fake as its transport.
// Extra opts are applied before the transport, matching [httpclient.New]
// semantics.
func (f *Fake) Client(opts ...httpclient.Option) *httpclient.Client {
	opts = append(opts, httpclient.WithHTTPClient(&http.Client{Transport: f}))
	return httpclient.New(opts...)
}

// GetRequests returns a defensive copy of the recorded requests in send order.
func (f *Fake) GetRequests() []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*http.Request, len(f.requests))
	copy(out, f.requests)
	return out
}

// AssertSent fails the test if no recorded request matches.
func (f *Fake) AssertSent(t *testing.T, match Matcher) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, req := range f.requests {
		if match(req) {
			return
		}
	}
	t.Errorf("httpclienttest: expected a matching request to have been sent, but none was (%d sent)", len(f.requests))
}

// AssertNotSent fails the test if any recorded request matches.
func (f *Fake) AssertNotSent(t *testing.T, match Matcher) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, req := range f.requests {
		if match(req) {
			t.Errorf("httpclienttest: expected no matching request, but %s %s was sent", req.Method, req.URL)
			return
		}
	}
}

// AssertNothingSent fails the test if any request was recorded.
func (f *Fake) AssertNothingSent(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 0 {
		t.Errorf("httpclienttest: expected no requests to have been sent, but %d were", len(f.requests))
	}
}

// NewResponse builds a 200-ready *http.Response with the given status code and
// body. The body is wrapped in a fresh reader on every clone, so the returned
// response is safe to register as a reusable stub.
func NewResponse(status int, body []byte) *http.Response {
	buf := make([]byte, len(body))
	copy(buf, body)
	return &http.Response{
		StatusCode:    status,
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        make(http.Header),
		Body:          io.NopCloser(bytes.NewReader(buf)),
		ContentLength: int64(len(buf)),
	}
}

// recordRequest returns a copy of req with its body read and restored, so the
// recorded request can be inspected without disturbing the in-flight one.
func recordRequest(req *http.Request) *http.Request {
	rec := req.Clone(req.Context())
	if req.Body != nil && req.Body != http.NoBody {
		body, err := io.ReadAll(io.LimitReader(req.Body, maxFakeBodyBytes)) //nolint:forbidigo // bounded by io.LimitReader
		if err == nil {
			req.Body = io.NopCloser(bytes.NewReader(body))
			rec.Body = io.NopCloser(bytes.NewReader(body))
		}
	}
	return rec
}

// cloneResponse returns a fresh, independent copy of resp so that concurrent
// callers each get their own readable body and never share Header maps.
func cloneResponse(resp *http.Response) *http.Response {
	clone := *resp

	clone.Header = resp.Header.Clone()
	if clone.Header == nil {
		clone.Header = make(http.Header)
	}

	var body []byte
	if resp.Body != nil {
		body, _ = io.ReadAll(io.LimitReader(resp.Body, maxFakeBodyBytes)) //nolint:forbidigo // bounded by io.LimitReader
		// Restore the source body so the stub can be served again.
		resp.Body = io.NopCloser(bytes.NewReader(body))
	}
	clone.Body = io.NopCloser(bytes.NewReader(body))
	clone.ContentLength = int64(len(body))
	return &clone
}
