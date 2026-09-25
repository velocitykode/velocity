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

// varyLists reports whether any of h's Vary values lists name.
func varyLists(h http.Header, name string) bool {
	for _, v := range h.Values("Vary") {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), name) {
				return true
			}
		}
	}
	return false
}

// TestInstall_VaryAcceptOnlyWhenTheRequestDecides asserts the pipeline
// lists Accept and X-Requested-With in Vary when the request's
// negotiation chose the JSON or HTML answer, and not when JSONWhen, API
// mode or an API prefix fixed it or the request is an Inertia request,
// while X-Inertia is listed on every negotiated answer.
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
			for _, name := range []string{"Accept", "X-Requested-With"} {
				if got := varyLists(w.Header(), name); got != tt.wantVary {
					t.Errorf("Vary lists %s = %v, want %v (Vary %q)", name, got, tt.wantVary, w.Header().Values("Vary"))
				}
			}
			if !varyLists(w.Header(), "X-Inertia") {
				t.Errorf("Vary does not list X-Inertia (Vary %q)", w.Header().Values("Vary"))
			}
		})
	}
}

// TestInstall_VaryListsTheHeaderThatChoseTheFormat asserts, through the
// standalone router and the pipeline, that two requests to one URL
// differing only in X-Requested-With (Accept */* or absent) or only in
// X-Inertia get different answers whose Vary both list that header, so a
// shared cache never serves one answer for the other request.
func TestInstall_VaryListsTheHeaderThatChoseTheFormat(t *testing.T) {
	tests := []struct {
		name         string
		configure    func(h *problem.Handler)
		accept       string
		header       string // the header the pair differs in
		value        string
		pipelineOnly bool // the standalone router answers both alike
	}{
		{name: "xhr accept any", accept: "*/*", header: "X-Requested-With", value: "XMLHttpRequest"},
		{name: "xhr no accept", header: "X-Requested-With", value: "XMLHttpRequest"},
		{name: "inertia html accept", accept: "text/html", header: "X-Inertia", value: "true", pipelineOnly: true},
		{name: "inertia json accept", accept: "application/json", header: "X-Inertia", value: "true"},
		{name: "inertia json when declines", configure: func(h *problem.Handler) {
			h.JSONWhen(func(*http.Request, error) bool { return false })
		}, accept: "text/html", header: "X-Inertia", value: "true", pipelineOnly: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := problem.NewHandler(problem.WithReporters())
			h.SetDebug(false)
			if tt.configure != nil {
				tt.configure(h)
			}
			installed := router.New()
			Install(installed, WithHandler(func() contract.ErrorHandler { return h }))
			installed.Get("/x", func(*router.Context) error { return problem.NotFound() })
			standalone := router.New()
			standalone.Get("/x", func(*router.Context) error { return problem.NotFound() })

			for name, r := range map[string]*router.VelocityRouterV2{"standalone": standalone, "pipeline": installed} {
				if tt.pipelineOnly && name == "standalone" {
					continue
				}
				var answers [2]*httptest.ResponseRecorder
				for i, with := range []bool{false, true} {
					req := httptest.NewRequest(http.MethodGet, "/x", nil)
					if tt.accept != "" {
						req.Header.Set("Accept", tt.accept)
					}
					if with {
						req.Header.Set(tt.header, tt.value)
					}
					answers[i] = httptest.NewRecorder()
					r.ServeHTTP(answers[i], req)
				}
				without, with := answers[0], answers[1]
				if without.Code == with.Code && without.Header().Get("Content-Type") == with.Header().Get("Content-Type") {
					t.Fatalf("%s: the pair got the same answer %d %q; the header chose nothing", name, with.Code, with.Header().Get("Content-Type"))
				}
				for label, w := range map[string]*httptest.ResponseRecorder{"without": without, "with": with} {
					if !varyLists(w.Header(), tt.header) {
						t.Errorf("%s %s %s: %d %q, Vary %q does not list %s", name, label, tt.header, w.Code, w.Header().Get("Content-Type"), w.Header().Values("Vary"), tt.header)
					}
				}
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
