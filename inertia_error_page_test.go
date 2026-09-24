package velocity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/view"
)

const inertiaTestTemplate = `<!DOCTYPE html><html><head>{{ .inertiaHead }}</head><body>{{ .inertia }}</body></html>`

// newInertiaApp builds an app with a view engine whose error page is
// errorPage ("" for none), debug rendering as given, the bond middleware
// installed globally and two routes: GET and POST /page fail with 404.
func newInertiaApp(t *testing.T, errorPage string, debug bool) *App {
	t.Helper()
	a, err := New(WithConfig(Config{
		Env:   "testing",
		Debug: debug,
		Port:  "0",
		Cache: CacheConfig{Driver: "memory", Prefix: "test_cache"},
		Log:   log.LogConfig{Driver: "null", Config: make(map[string]any)},
		Queue: QueueConfig{Driver: "memory"},
		Mail:  mail.MailConfig{Driver: "log"},
		View:  view.Config{RootTemplate: inertiaTestTemplate, Version: "v1", ErrorPage: errorPage},
	}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	a.Errors(func(h contract.ErrorHandler) { h.SetDebug(debug) })
	if err := a.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	engine, ok := a.Services.View.(*view.Engine)
	if !ok {
		t.Fatal("view engine not built")
	}
	a.Router.Use(engine.Middleware())
	notFound := func(*router.Context) error { return problem.NotFound() }
	a.Router.Get("/page", notFound)
	a.Router.Post("/page", notFound)
	return a
}

// inertiaPage decodes the page object of an Inertia JSON body or of the
// data-page script of an HTML shell.
func inertiaPage(t *testing.T, w *httptest.ResponseRecorder, xhr bool) map[string]any {
	t.Helper()
	raw := w.Body.String()
	if !xhr {
		const marker = `type="application/json" data-page="app">`
		start := strings.Index(raw, marker)
		if start < 0 {
			t.Fatalf("no page script in body: %s", raw)
		}
		raw = raw[start+len(marker):]
		raw = raw[:strings.Index(raw, "</script>")]
	}
	var page map[string]any
	if err := json.Unmarshal([]byte(raw), &page); err != nil {
		t.Fatalf("decode page: %v (%s)", err, raw)
	}
	return page
}

// TestInertiaErrorPage drives the three tiers of an Inertia request that
// fails (error component, 409 reload, debug page) and the full-page visit
// through a real app: router boundary, bond middleware, error pipeline and
// view engine.
func TestInertiaErrorPage(t *testing.T) {
	tests := []struct {
		name         string
		errorPage    string
		debug        bool
		method       string
		path         string
		headers      map[string]string
		wantStatus   int
		wantPage     bool
		wantXHR      bool
		wantLocation string
		wantContent  string
		wantMessage  string
	}{
		{
			name: "XHRRouteErrorRendersComponent", errorPage: "Error", method: http.MethodGet, path: "/page",
			headers:    map[string]string{"X-Inertia": "true", "X-Inertia-Version": "v1"},
			wantStatus: http.StatusNotFound, wantPage: true, wantXHR: true, wantContent: "application/json",
			wantMessage: "Not Found",
		},
		{
			name: "XHRUnmatchedRouteRendersComponent", errorPage: "Error", method: http.MethodGet, path: "/nowhere",
			headers:    map[string]string{"X-Inertia": "true"},
			wantStatus: http.StatusNotFound, wantPage: true, wantXHR: true, wantContent: "application/json",
			wantMessage: "Not Found",
		},
		{
			name: "FullPageRouteErrorRendersComponent", errorPage: "Error", method: http.MethodGet, path: "/page",
			headers:    map[string]string{"Accept": "text/html"},
			wantStatus: http.StatusNotFound, wantPage: true, wantContent: "text/html; charset=utf-8",
			wantMessage: "Not Found",
		},
		{
			name: "FullPageUnmatchedRouteRendersComponent", errorPage: "Error", method: http.MethodGet, path: "/nowhere",
			headers:    map[string]string{"Accept": "text/html"},
			wantStatus: http.StatusNotFound, wantPage: true, wantContent: "text/html; charset=utf-8",
			wantMessage: "Not Found",
		},
		{
			name: "NoComponentGETReloadsCurrentURL", method: http.MethodGet, path: "/page?tab=a",
			headers:    map[string]string{"X-Inertia": "true", "X-Inertia-Version": "v1"},
			wantStatus: http.StatusConflict, wantLocation: "/page?tab=a",
		},
		{
			name: "NoComponentPOSTReloadsReferer", method: http.MethodPost, path: "/page",
			headers:    map[string]string{"X-Inertia": "true", "Referer": "/posts/new?draft=1"},
			wantStatus: http.StatusConflict, wantLocation: "/posts/new?draft=1",
		},
		{
			name: "NoComponentPOSTSameHostReferer", method: http.MethodPost, path: "/page",
			headers:    map[string]string{"X-Inertia": "true", "Referer": "http://example.com/posts/new"},
			wantStatus: http.StatusConflict, wantLocation: "http://example.com/posts/new",
		},
		{
			name: "NoComponentPOSTForeignReferer", method: http.MethodPost, path: "/page",
			headers:    map[string]string{"X-Inertia": "true", "Referer": "https://evil.test/phish"},
			wantStatus: http.StatusConflict, wantLocation: "/",
		},
		{
			name: "AssetVersionMismatchStill409", errorPage: "Error", method: http.MethodGet, path: "/page",
			headers:    map[string]string{"X-Inertia": "true", "X-Inertia-Version": "stale"},
			wantStatus: http.StatusConflict, wantLocation: "/page",
		},
		{
			name: "DebugXHRRendersDebugPageAtRealStatus", errorPage: "Error", debug: true, method: http.MethodGet, path: "/page",
			headers:    map[string]string{"X-Inertia": "true", "X-Inertia-Version": "v1"},
			wantStatus: http.StatusNotFound, wantContent: "text/html; charset=utf-8",
		},
		{
			name: "DebugFullPageRendersDebugPageAtRealStatus", errorPage: "Error", debug: true, method: http.MethodGet, path: "/page",
			headers:    map[string]string{"Accept": "text/html"},
			wantStatus: http.StatusNotFound, wantContent: "text/html; charset=utf-8",
		},
		{
			name: "JSONClientGetsProblemJSON", errorPage: "Error", method: http.MethodGet, path: "/page",
			headers:    map[string]string{"Accept": "application/json"},
			wantStatus: http.StatusNotFound, wantContent: problem.ProblemTypeContent,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newInertiaApp(t, tt.errorPage, tt.debug)
			req := httptest.NewRequest(tt.method, tt.path, nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			a.Router.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", w.Code, tt.wantStatus, w.Body.String())
			}
			if got := w.Header().Get("X-Inertia-Location"); got != tt.wantLocation {
				t.Errorf("X-Inertia-Location = %q, want %q", got, tt.wantLocation)
			}
			if tt.wantContent != "" && w.Header().Get("Content-Type") != tt.wantContent {
				t.Errorf("Content-Type = %q, want %q", w.Header().Get("Content-Type"), tt.wantContent)
			}
			isComponent := strings.Contains(w.Body.String(), `"component":"Error"`)
			if isComponent != tt.wantPage {
				t.Fatalf("error component rendered = %v, want %v (body %q)", isComponent, tt.wantPage, w.Body.String())
			}
			if !tt.wantPage {
				return
			}
			page := inertiaPage(t, w, tt.wantXHR)
			props, _ := page["props"].(map[string]any)
			if status, _ := props["status"].(float64); int(status) != http.StatusNotFound {
				t.Errorf("props.status = %v, want 404", props["status"])
			}
			if props["message"] != tt.wantMessage {
				t.Errorf("props.message = %v, want %q", props["message"], tt.wantMessage)
			}
			if tt.wantXHR && w.Header().Get("X-Inertia") != "true" {
				t.Errorf("X-Inertia header missing on the page object response")
			}
		})
	}
}

func TestConfigFromEnv_ViewErrorPage(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "Unset", value: "", want: ""},
		{name: "Component", value: "Error", want: "Error"},
		{name: "Trimmed", value: "  Errors/Show ", want: "Errors/Show"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("VIEW_ERROR_PAGE", tt.value)
			if got := ConfigFromEnv().View.ErrorPage; got != tt.want {
				t.Errorf("View.ErrorPage = %q, want %q", got, tt.want)
			}
		})
	}
}
