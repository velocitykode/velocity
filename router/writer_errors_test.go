package router

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

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
	// textBody is the exact plain-text body the default handler writes.
	textBody string
	// detail is the problem+json detail the default handler writes.
	detail string
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
	}
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
			for k, v := range tc.headers {
				if got := w.Header().Values(k); len(got) != 1 || got[0] != v {
					t.Errorf("%s = %q, want exactly [%q]", k, got, v)
				}
			}
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
			for k, v := range tc.headers {
				if got := he.Headers().Values(k); len(got) != 1 || got[0] != v {
					t.Errorf("error header %s = %q, want exactly [%q]", k, got, v)
				}
			}
			if call.info.Committed || call.info.Recovered {
				t.Errorf("info = %+v, want neither committed nor recovered", call.info)
			}
			if w.Body.Len() != 0 {
				t.Errorf("router wrote %q with a handler installed, want nothing", w.Body.String())
			}
			for k := range tc.headers {
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
				Instance: req.URL.EscapedPath(),
			}
			if body != want {
				t.Errorf("problem = %+v, want %+v", body, want)
			}
			for k, v := range tc.headers {
				if got := w.Header().Values(k); len(got) != 1 || got[0] != v {
					t.Errorf("%s = %q, want exactly [%q]", k, got, v)
				}
			}
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
