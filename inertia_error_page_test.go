package velocity

import (
	"context"
	"encoding/json"
	"errors"
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
	a, _ := newInertiaTestApp(t, errorPage, debug, true)
	notFound := func(*router.Context) error { return problem.NotFound() }
	a.Router.Get("/page", notFound)
	a.Router.Post("/page", notFound)
	return a
}

// newInertiaTestApp builds a bootstrapped app, its error pipeline wired
// through the router boundary, with a view engine whose error page is
// errorPage ("" for none) and debug rendering as given. The bond middleware
// is installed globally when withBond is set, as the starter templates do.
func newInertiaTestApp(t *testing.T, errorPage string, debug, withBond bool) (*App, *view.Engine) {
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
	if withBond {
		a.Router.Use(engine.Middleware())
	}
	return a, engine
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
			wantStatus: http.StatusConflict, wantLocation: "/posts/new",
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

// TestInertiaErrorPage_EncodeFailure drives the tiers TestInertiaErrorPage
// covers (error component, 409 reload, debug page) from an Inertia request
// whose own page render fails while encoding the page object: a prop JSON
// cannot encode (a channel here; NaN, Inf or a failing MarshalJSON take the
// same path).
//
// X-Inertia: true marks a page-object response. The Inertia client tests
// it before anything else and, when it is present, processes the body as a
// page, so the 409 location reload and the debug page are reachable only
// on a response without it. A page render that fails must leave neither
// the marker nor its JSON Content-Type behind: only the error component's
// page object carries them, and the reload and the debug page answer
// exactly as they do for a plain returned error.
func TestInertiaErrorPage_EncodeFailure(t *testing.T) {
	render := func(props view.Props) router.HandlerFunc {
		return func(c *router.Context) error { return view.Render(c, "Report", props) }
	}
	returned := func(*router.Context) error { return errors.New("report failed") }

	const reload = "/report?tab=a"
	tests := []struct {
		name         string
		errorPage    string
		debug        bool
		withoutBond  bool // the bond middleware is not installed
		sharedBad    bool // an unencodable shared prop also fails the error page
		handler      router.HandlerFunc
		wantStatus   int
		wantInertia  bool
		wantPage     bool
		wantLocation string
		wantContent  string
	}{
		{
			// Control: the reload for a plain returned error carries no marker.
			name: "ReturnedErrorReloads", handler: returned,
			wantStatus: http.StatusConflict, wantLocation: reload,
		},
		{
			name: "EncodeFailureReloads", handler: render(view.Props{"feed": make(chan int)}),
			wantStatus: http.StatusConflict, wantLocation: reload,
		},
		{
			name: "EncodeFailureWithoutBondMiddlewareReloads", withoutBond: true,
			handler:    render(view.Props{"feed": make(chan int)}),
			wantStatus: http.StatusConflict, wantLocation: reload,
		},
		{
			name: "EncodeFailureErrorPageAlsoFailsReloads", errorPage: "Error", sharedBad: true,
			handler:    render(view.Props{"total": 1}),
			wantStatus: http.StatusConflict, wantLocation: reload,
		},
		{
			name: "EncodeFailureDebugRendersDebugPage", errorPage: "Error", debug: true,
			handler:    render(view.Props{"feed": make(chan int)}),
			wantStatus: http.StatusInternalServerError, wantContent: "text/html; charset=utf-8",
		},
		{
			// Control: the error component's page object is a page, marker and all.
			name: "EncodeFailureRendersErrorComponent", errorPage: "Error",
			handler:    render(view.Props{"feed": make(chan int)}),
			wantStatus: http.StatusInternalServerError, wantInertia: true, wantPage: true,
			wantContent: "application/json",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, engine := newInertiaTestApp(t, tt.errorPage, tt.debug, !tt.withoutBond)
			a.Router.Get("/report", tt.handler)
			if tt.sharedBad {
				engine.Share("feed", make(chan int))
			}
			req := httptest.NewRequest(http.MethodGet, reload, nil)
			req.Header.Set("X-Inertia", "true")
			req.Header.Set("X-Inertia-Version", "v1")
			w := httptest.NewRecorder()
			a.Router.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", w.Code, tt.wantStatus, w.Body.String())
			}
			if got := w.Header().Get("X-Inertia-Location"); got != tt.wantLocation {
				t.Errorf("X-Inertia-Location = %q, want %q", got, tt.wantLocation)
			}
			if got := w.Header().Get("Content-Type"); got != tt.wantContent {
				t.Errorf("Content-Type = %q, want %q", got, tt.wantContent)
			}
			if isPage := strings.Contains(w.Body.String(), `"component":"Error"`); isPage != tt.wantPage {
				t.Errorf("error component rendered = %v, want %v (body %q)", isPage, tt.wantPage, w.Body.String())
			}
			got := w.Header().Get("X-Inertia")
			switch {
			case tt.wantInertia && got != "true":
				t.Errorf("X-Inertia = %q on the error component's page object, want %q", got, "true")
			case !tt.wantInertia && got != "":
				t.Errorf("X-Inertia = %q on the %d answer (Content-Type %q, X-Inertia-Location %q), want it absent: "+
					"the marker left by the failed page render tells the Inertia client this is a page object, "+
					"so it never takes the reload or debug handling", got, w.Code,
					w.Header().Get("Content-Type"), w.Header().Get("X-Inertia-Location"))
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
