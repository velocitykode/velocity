package velocity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/view"
)

// setViewTestEnv points ConfigFromEnv at in-memory drivers and sets the
// VIEW_* variables to view (unset ones empty).
func setViewTestEnv(t *testing.T, view map[string]string) {
	t.Helper()
	for k, v := range map[string]string{
		"APP_ENV":       "testing",
		"APP_DEBUG":     "false",
		"LOG_DRIVER":    "null",
		"CACHE_DRIVER":  "memory",
		"QUEUE_DRIVER":  "memory",
		"MAIL_DRIVER":   "log",
		"DB_CONNECTION": "",
	} {
		t.Setenv(k, v)
	}
	for _, k := range []string{"VIEW_ERROR_PAGE", "VIEW_SSR_ENABLED", "VIEW_SSR_URL", "VIEW_SSR_TIMEOUT", "VIEW_SSR_EXCEPT"} {
		t.Setenv(k, view[k])
	}
}

// TestNew_BuildsViewEngineFromEnv asserts New builds the view engine from
// the environment alone when VIEW_ERROR_PAGE or VIEW_SSR_ENABLED is set
// (no code sets RootTemplate), and builds none when neither is.
func TestNew_BuildsViewEngineFromEnv(t *testing.T) {
	tests := []struct {
		name          string
		env           map[string]string
		wantEngine    bool
		wantComponent string
	}{
		{name: "error page only", env: map[string]string{"VIEW_ERROR_PAGE": "Error"}, wantEngine: true, wantComponent: "Error"},
		{name: "ssr enabled only", env: map[string]string{"VIEW_SSR_ENABLED": "true"}, wantEngine: true},
		{name: "none set", env: map[string]string{}, wantEngine: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setViewTestEnv(t, tt.env)
			a, err := New()
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
			engine, ok := a.Services.View.(*view.Engine)
			if ok != tt.wantEngine {
				t.Fatalf("view engine built = %v (Services.View %T), want %v", ok, a.Services.View, tt.wantEngine)
			}
			if ok {
				if got := engine.Bond().ErrorComponent(); got != tt.wantComponent {
					t.Errorf("ErrorComponent = %q, want %q", got, tt.wantComponent)
				}
			}
		})
	}
}

// TestNew_EnvErrorPageRendersForInertia asserts an app configured only by
// VIEW_ERROR_PAGE answers an Inertia request failing with 404 with the
// error page component at the real status.
func TestNew_EnvErrorPageRendersForInertia(t *testing.T) {
	setViewTestEnv(t, map[string]string{"VIEW_ERROR_PAGE": "Error"})
	a, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	if err := a.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	a.Router.Get("/page", func(*router.Context) error { return problem.NotFound() })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/page", nil)
	req.Header.Set("X-Inertia", "true")
	a.Router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", w.Code, w.Body.String())
	}
	var page struct {
		Component string         `json:"component"`
		Props     map[string]any `json:"props"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("body is not an Inertia page: %v (%s)", err, w.Body.String())
	}
	if page.Component != "Error" || page.Props["status"] != float64(http.StatusNotFound) {
		t.Errorf("page = %+v, want component Error with status 404", page)
	}
}
