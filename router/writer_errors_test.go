package router

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
)

// Wire bytes of the unmatched responses captured before the terminal
// returned errors (http.NotFound, and http.Error with the 405 status
// text). A text client of a standalone router must keep seeing exactly
// these.
const (
	unmatchedNotFoundBody         = "404 page not found\n"
	unmatchedMethodNotAllowedBody = "Method Not Allowed\n"
	plainTextContentType          = "text/plain; charset=utf-8"
)

// writerCase is one router-owned error answer: how to build the router
// and the request that triggers it, and what the error carries.
type writerCase struct {
	name  string
	setup func(r *VelocityRouterV2)
	// request builds the triggering request.
	request func() *http.Request
	status  int
	// headers must be present with exactly these values.
	headers map[string]string
	// numericHeaders must be present with a non-negative integer value
	// (time-dependent values such as Retry-After).
	numericHeaders []string
	// textBody is the exact plain-text body the default handler writes.
	textBody string
	// detail is the problem+json detail the default handler writes.
	detail string
	// is, when set, must match the returned error under errors.Is.
	is error
}

func writerCases() []writerCase {
	return []writerCase{
		{
			name:     "unmatched path",
			setup:    func(r *VelocityRouterV2) { r.Post("/mcp", okHandler) },
			request:  func() *http.Request { return httptest.NewRequest(http.MethodGet, "/nope", nil) },
			status:   http.StatusNotFound,
			textBody: unmatchedNotFoundBody,
			detail:   "404 page not found",
		},
		{
			name: "unmatched method",
			setup: func(r *VelocityRouterV2) {
				r.Post("/mcp", okHandler)
				r.Put("/mcp", okHandler)
			},
			request:  func() *http.Request { return httptest.NewRequest(http.MethodGet, "/mcp", nil) },
			status:   http.StatusMethodNotAllowed,
			headers:  map[string]string{"Allow": "POST, PUT"},
			textBody: unmatchedMethodNotAllowedBody,
			detail:   "Method Not Allowed",
		},
		{
			name: "rate limit",
			setup: func(r *VelocityRouterV2) {
				r.Use(RateLimit(1, time.Minute, WithBurst(0)))
				r.Get("/limited", okHandler)
			},
			request: func() *http.Request { return httptest.NewRequest(http.MethodGet, "/limited", nil) },
			status:  http.StatusTooManyRequests,
			headers: map[string]string{
				"X-RateLimit-Limit":     "1",
				"X-RateLimit-Remaining": "0",
			},
			numericHeaders: []string{"Retry-After", "X-RateLimit-Reset"},
			textBody:       "Rate limit exceeded\n",
			detail:         "Rate limit exceeded",
		},
		{
			name: "timeout",
			setup: func(r *VelocityRouterV2) {
				r.Use(Timeout(20 * time.Millisecond))
				r.Get("/slow", func(c *Context) error {
					<-c.Request.Context().Done()
					return nil
				})
			},
			request:  func() *http.Request { return httptest.NewRequest(http.MethodGet, "/slow", nil) },
			status:   http.StatusServiceUnavailable,
			textBody: "Service Unavailable\n",
			detail:   "Service Unavailable",
			is:       context.DeadlineExceeded,
		},
		{
			name: "unsupported media type",
			setup: func(r *VelocityRouterV2) {
				r.Use(ContentTypeJSON())
				r.Post("/items", okHandler)
			},
			request: func() *http.Request {
				req := httptest.NewRequest(http.MethodPost, "/items", strings.NewReader("a=b"))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				return req
			},
			status:   http.StatusUnsupportedMediaType,
			textBody: "Unsupported Media Type\n",
			detail:   "Unsupported Media Type",
		},
	}
}

// checkHeaders asserts h carries tc's exact and numeric headers, each
// exactly once.
func checkHeaders(t *testing.T, where string, h http.Header, tc writerCase) {
	t.Helper()
	for k, v := range tc.headers {
		if got := h.Values(k); len(got) != 1 || got[0] != v {
			t.Errorf("%s %s = %q, want exactly [%q]", where, k, got, v)
		}
	}
	for _, k := range tc.numericHeaders {
		got := h.Values(k)
		if len(got) != 1 {
			t.Errorf("%s %s = %q, want exactly one value", where, k, got)
			continue
		}
		if n, err := strconv.ParseInt(got[0], 10, 64); err != nil || n < 0 {
			t.Errorf("%s %s = %q, want a non-negative integer", where, k, got[0])
		}
	}
}

// caseHeaderKeys returns every header name tc names.
func caseHeaderKeys(tc writerCase) []string {
	keys := append([]string(nil), tc.numericHeaders...)
	for k := range tc.headers {
		keys = append(keys, k)
	}
	return keys
}

// serveWriterCase builds a fresh router for tc, lets install adjust it,
// and serves the case's request with accept as its Accept header.
func serveWriterCase(tc writerCase, accept string, install func(r *VelocityRouterV2)) *httptest.ResponseRecorder {
	r := NewV2()
	tc.setup(r)
	if install != nil {
		install(r)
	}
	req := tc.request()
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestRouterWriters_StandaloneText(t *testing.T) {
	for _, tc := range writerCases() {
		t.Run(tc.name, func(t *testing.T) {
			w := serveWriterCase(tc, "", nil)

			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
			if got := w.Body.String(); got != tc.textBody {
				t.Errorf("body = %q, want %q", got, tc.textBody)
			}
			if got := w.Header().Get("Content-Type"); got != plainTextContentType {
				t.Errorf("Content-Type = %q, want %q", got, plainTextContentType)
			}
			if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
			}
			checkHeaders(t, "response", w.Header(), tc)
		})
	}
}

func TestRouterWriters_SeamReceivesHTTPError(t *testing.T) {
	for _, tc := range writerCases() {
		t.Run(tc.name, func(t *testing.T) {
			seam := &seamRecorder{}
			w := serveWriterCase(tc, "", func(r *VelocityRouterV2) { r.SetErrorHandler(seam.fn) })

			calls := seam.get()
			if len(calls) != 1 {
				t.Fatalf("error handler called %d times, want 1", len(calls))
			}
			call := calls[0]
			var he *contract.HTTPError
			if !errors.As(call.err, &he) {
				t.Fatalf("error %v (%T) is not a *contract.HTTPError", call.err, call.err)
			}
			if he.StatusCode() != tc.status {
				t.Errorf("StatusCode() = %d, want %d", he.StatusCode(), tc.status)
			}
			if tc.is != nil && !errors.Is(call.err, tc.is) {
				t.Errorf("error %v does not match %v", call.err, tc.is)
			}
			checkHeaders(t, "error", he.Headers(), tc)
			if call.info.Committed || call.info.Recovered {
				t.Errorf("info = %+v, want neither committed nor recovered", call.info)
			}
			if w.Body.Len() != 0 {
				t.Errorf("router wrote %q with a handler installed, want nothing", w.Body.String())
			}
			for _, k := range caseHeaderKeys(tc) {
				if got := w.Header().Get(k); got != "" {
					t.Errorf("router set %s = %q on the response, want it left to the handler", k, got)
				}
			}
		})
	}
}

func TestRouterWriters_JSONProblem(t *testing.T) {
	for _, tc := range writerCases() {
		t.Run(tc.name, func(t *testing.T) {
			w := serveWriterCase(tc, "application/json", nil)

			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
			if got := w.Header().Get("Content-Type"); got != "application/problem+json" {
				t.Fatalf("Content-Type = %q, want application/problem+json", got)
			}
			var body problemBody
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("body %q is not JSON: %v", w.Body.String(), err)
			}
			req := tc.request()
			want := problemBody{
				Type:     "about:blank",
				Title:    http.StatusText(tc.status),
				Status:   tc.status,
				Detail:   tc.detail,
				Instance: req.URL.Path,
			}
			if body != want {
				t.Errorf("problem = %+v, want %+v", body, want)
			}
			checkHeaders(t, "response", w.Header(), tc)
		})
	}
}

// Global middleware runs before the unmatched terminal and now sees the
// 404 / 405 come back up the chain as an error, so a group-wide
// ErrorHandlerMiddleware or a logging middleware can act on it.
func TestUnmatched_GlobalMiddlewareSeesTheError(t *testing.T) {
	tests := []struct {
		name   string
		target string
		status int
		allow  string
	}{
		{name: "404", target: "/nope", status: http.StatusNotFound},
		{name: "405", target: "/mcp", status: http.StatusMethodNotAllowed, allow: "POST"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen error
			runs := 0
			r := NewV2()
			r.Use(func(next HandlerFunc) HandlerFunc {
				return func(c *Context) error {
					runs++
					err := next(c)
					seen = err
					return err
				}
			})
			r.Post("/mcp", okHandler)

			w := serve(r, http.MethodGet, tt.target)

			if runs != 1 {
				t.Fatalf("global middleware ran %d times, want 1", runs)
			}
			var he *contract.HTTPError
			if !errors.As(seen, &he) || he.StatusCode() != tt.status {
				t.Fatalf("middleware saw %v, want a %d *contract.HTTPError", seen, tt.status)
			}
			if got := he.Headers().Get("Allow"); got != tt.allow {
				t.Errorf("error Allow = %q, want %q", got, tt.allow)
			}
			if w.Code != tt.status {
				t.Errorf("status = %d, want %d", w.Code, tt.status)
			}
		})
	}
}

// A handler that keeps running past its deadline and keeps writing
// (status, headers, body) must never produce a second response: the error
// boundary writes the one answer and the late writes are discarded. The
// handler starts writing the moment its deadline fires and does not stop
// until the request is over, so its writes overlap the boundary's under
// -race.
func TestTimeout_LateWriterProducesOneBody(t *testing.T) {
	tests := []struct {
		name       string
		accept     string
		seam       bool
		wantStatus int
		wantBody   string
	}{
		{name: "text client, default handler", wantStatus: http.StatusServiceUnavailable, wantBody: "Service Unavailable\n"},
		{name: "json client, default handler", accept: "application/json", wantStatus: http.StatusServiceUnavailable},
		{name: "installed error handler", seam: true, wantStatus: http.StatusInternalServerError, wantBody: "seam body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stop := make(chan struct{})
			finished := make(chan error, 1)

			r := NewV2()
			r.Use(Timeout(20 * time.Millisecond))
			if tt.seam {
				seam := &seamRecorder{body: "seam body"}
				r.SetErrorHandler(seam.fn)
			}
			r.Get("/late", func(c *Context) error {
				<-c.Request.Context().Done()
				for {
					c.Response.Header().Set("X-Late", "1")
					c.Response.WriteHeader(http.StatusTeapot)
					_, err := c.Response.Write([]byte("late body"))
					select {
					case <-stop:
						finished <- err
						return errors.New("late handler error")
					default:
						runtime.Gosched()
					}
				}
			})

			req := httptest.NewRequest(http.MethodGet, "/late", nil)
			if tt.accept != "" {
				req.Header.Set("Accept", tt.accept)
			}
			w := newHeaderCountingRecorder()
			r.ServeHTTP(w, req)

			close(stop)
			select {
			case err := <-finished:
				if !errors.Is(err, ErrHandlerTimeout) {
					t.Errorf("late Write error = %v, want ErrHandlerTimeout", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("late handler never finished")
			}

			if w.headerCalls != 1 {
				t.Errorf("status line written %d times, want 1", w.headerCalls)
			}
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			body := w.Body.String()
			if strings.Contains(body, "late body") || w.Header().Get("X-Late") != "" {
				t.Errorf("late handler output reached the wire: body %q, X-Late %q", body, w.Header().Get("X-Late"))
			}
			if tt.wantBody != "" && body != tt.wantBody {
				t.Errorf("body = %q, want exactly %q", body, tt.wantBody)
			}
			if tt.accept != "" {
				var p problemBody
				if err := json.Unmarshal([]byte(body), &p); err != nil || p.Status != tt.wantStatus {
					t.Errorf("body %q is not one problem document for %d (%v)", body, tt.wantStatus, err)
				}
			}
		})
	}
}

// A deadline is a warning, not a failure the error logger owns: the
// default path logs the 503 once at warn level and RequestFailed carries
// the deadline.
func TestTimeout_DefaultPathLogsWarnAndFails(t *testing.T) {
	var mu sync.Mutex
	var warns, errs int
	collector := newTestEventCollector()

	r := NewV2()
	r.SetWarnLogger(func(string, ...any) { mu.Lock(); warns++; mu.Unlock() })
	r.SetErrorLogger(func(string, ...any) { mu.Lock(); errs++; mu.Unlock() })
	r.SetEventDispatcher(collector.dispatch)
	r.Use(Timeout(20 * time.Millisecond))
	r.Get("/slow", func(c *Context) error {
		<-c.Request.Context().Done()
		return nil
	})

	w := serve(r, http.MethodGet, "/slow")

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	mu.Lock()
	gotWarns, gotErrs := warns, errs
	mu.Unlock()
	if gotWarns != 1 || gotErrs != 0 {
		t.Errorf("warn logs = %d, error logs = %d, want 1 and 0", gotWarns, gotErrs)
	}
	var failed *RequestFailed
	for _, e := range collector.getEvents() {
		if ev, ok := e.(*RequestFailed); ok {
			failed = ev
		}
	}
	if failed == nil || !errors.Is(failed.Error, context.DeadlineExceeded) {
		t.Errorf("RequestFailed = %+v, want one carrying context.DeadlineExceeded", failed)
	}
}

// When the client goes away before the deadline, the Timeout error wraps
// context.Canceled on a dead request context and the default boundary
// writes nothing: there is no one to answer.
func TestTimeout_ClientGoneWritesNothing(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)

	r := NewV2()
	r.Use(Timeout(time.Minute))
	r.Get("/slow", func(c *Context) error {
		close(started)
		<-release
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/slow", nil).WithContext(ctx)
	go func() {
		<-started
		cancel()
	}()
	w := newHeaderCountingRecorder()
	r.ServeHTTP(w, req)

	if w.headerCalls != 0 || w.Body.Len() != 0 {
		t.Errorf("wrote %d status lines and body %q to a gone client, want nothing", w.headerCalls, w.Body.String())
	}
}
