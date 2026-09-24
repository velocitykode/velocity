package routerbridge

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/router"
)

// TestInstall_ContextWantsJSONAgreesWithPipeline asserts ctx.WantsJSON,
// with the pipeline wired as Services.Errors, gives the answer the
// pipeline renders the handler's error in: API prefixes, API mode and
// JSONWhen decide before the request's Accept header.
func TestInstall_ContextWantsJSONAgreesWithPipeline(t *testing.T) {
	tests := []struct {
		name      string
		configure func(h *problem.Handler)
		path      string
		accept    string
		wantJSON  bool
	}{
		{name: "api prefix, browser accept", configure: func(h *problem.Handler) { h.SetAPIPrefixes("/api") }, path: "/api/x", accept: "*/*", wantJSON: true},
		{name: "outside the prefix", configure: func(h *problem.Handler) { h.SetAPIPrefixes("/api") }, path: "/web/x", accept: "text/html", wantJSON: false},
		{name: "api mode", configure: func(h *problem.Handler) { h.SetAPIMode(true) }, path: "/web/x", accept: "text/html", wantJSON: true},
		{name: "json when html", configure: func(h *problem.Handler) {
			h.JSONWhen(func(*http.Request, error) bool { return false })
		}, path: "/web/x", accept: "application/json", wantJSON: false},
		{name: "request decides by q", configure: func(*problem.Handler) {}, path: "/web/x", accept: "text/html;q=0.9, application/json", wantJSON: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := problem.NewHandler(problem.WithReporters())
			h.SetDebug(false)
			tt.configure(h)
			r := router.New()
			r.SetServices(&app.Services{Errors: h})
			Install(r, WithHandler(func() contract.ErrorHandler { return h }))
			var handlerJSON bool
			r.Get(tt.path, func(c *router.Context) error {
				handlerJSON = c.WantsJSON()
				return problem.NotFound()
			})
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Header.Set("Accept", tt.accept)
			r.ServeHTTP(w, req)
			pipelineJSON := strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json")
			if handlerJSON != tt.wantJSON || pipelineJSON != tt.wantJSON {
				t.Errorf("ctx.WantsJSON = %v, pipeline JSON = %v (Content-Type %q), want %v", handlerJSON, pipelineJSON, w.Header().Get("Content-Type"), tt.wantJSON)
			}
		})
	}
}

// TestInstall_VaryAcceptOnlyWhenTheRequestDecides asserts the pipeline
// lists Accept in Vary when the request's negotiation chose the JSON or
// HTML answer, and not when JSONWhen, API mode or an API prefix fixed it
// or the request is an Inertia request.
func TestInstall_VaryAcceptOnlyWhenTheRequestDecides(t *testing.T) {
	tests := []struct {
		name      string
		configure func(h *problem.Handler)
		path      string
		accept    string
		inertia   bool
		wantVary  bool
	}{
		{name: "request picks json", configure: func(*problem.Handler) {}, path: "/x", accept: "application/json", wantVary: true},
		{name: "request picks html", configure: func(*problem.Handler) {}, path: "/x", accept: "text/html", wantVary: true},
		{name: "api prefix", configure: func(h *problem.Handler) { h.SetAPIPrefixes("/api") }, path: "/api/x", accept: "application/json"},
		{name: "api mode", configure: func(h *problem.Handler) { h.SetAPIMode(true) }, path: "/x", accept: "application/json"},
		{name: "json when", configure: func(h *problem.Handler) {
			h.JSONWhen(func(*http.Request, error) bool { return true })
		}, path: "/x", accept: "text/html"},
		{name: "inertia", configure: func(*problem.Handler) {}, path: "/x", accept: "text/html", inertia: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := problem.NewHandler(problem.WithReporters())
			h.SetDebug(false)
			tt.configure(h)
			r := router.New()
			Install(r, WithHandler(func() contract.ErrorHandler { return h }))
			r.Get(tt.path, func(*router.Context) error { return problem.NotFound() })
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Header.Set("Accept", tt.accept)
			if tt.inertia {
				req.Header.Set("X-Inertia", "true")
			}
			r.ServeHTTP(w, req)
			if w.Code != http.StatusNotFound && !(tt.inertia && w.Code == http.StatusConflict) {
				t.Fatalf("status = %d", w.Code)
			}
			vary := false
			for _, v := range w.Header().Values("Vary") {
				for _, part := range strings.Split(v, ",") {
					if strings.EqualFold(strings.TrimSpace(part), "Accept") {
						vary = true
					}
				}
			}
			if vary != tt.wantVary {
				t.Errorf("Vary lists Accept = %v, want %v (Vary %v)", vary, tt.wantVary, w.Header().Values("Vary"))
			}
		})
	}
}

// TestInstall_XInertiaOneIsNotInertia asserts a request with X-Inertia: 1
// takes the pipeline's JSON or HTML branch (not the Inertia branch), the
// same answer bond gives it (a full page, not an Inertia response).
func TestInstall_XInertiaOneIsNotInertia(t *testing.T) {
	h := problem.NewHandler(problem.WithReporters())
	h.SetDebug(false)
	r := router.New()
	Install(r, WithHandler(func() contract.ErrorHandler { return h }))
	r.Get("/x", func(*router.Context) error { return problem.NotFound() })
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-Inertia", "1")
	req.Header.Set("Accept", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") {
		t.Errorf("status %d Content-Type %q, want 404 problem+json", w.Code, w.Header().Get("Content-Type"))
	}
}
