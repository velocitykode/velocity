package view

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/router"
)

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	engine, err := NewEngine(Config{
		RootTemplate: defaultTemplate,
		Version:      "1.0",
	})
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}
	return engine
}

func TestNewEngine(t *testing.T) {
	config := Config{
		RootTemplate: `<html><body>{{ .inertia }}</body></html>`,
		Version:      "test-version",
	}

	engine, err := NewEngine(config)
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}

	// Should not panic - instance is initialized
	if engine.Bond() == nil {
		t.Error("Expected Bond instance to be set")
	}
}

func TestNewEngine_DefaultTemplate(t *testing.T) {
	config := Config{
		Version: "1.0",
	}

	_, err := NewEngine(config)
	if err != nil {
		t.Fatalf("NewEngine failed with default template: %v", err)
	}
}

func TestRender_HTMLResponse(t *testing.T) {
	engine, err := NewEngine(Config{
		RootTemplate: `<html><body>{{ .inertia }}</body></html>`,
		Version:      "1.0",
	})
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}

	req := httptest.NewRequest("GET", "/users", nil)
	rec := httptest.NewRecorder()

	props := Props{
		"users": []map[string]interface{}{
			{"id": 1, "name": "John"},
			{"id": 2, "name": "Jane"},
		},
	}

	err = engine.Render(rec, req, "Users/Index", props)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	contentType := rec.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/html") {
		t.Errorf("Expected HTML content type, got: %s", contentType)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "data-page") {
		t.Errorf("Expected HTML to contain data-page attribute. Got: %s", body)
	}

	if !strings.Contains(body, "Users/Index") {
		t.Errorf("Expected HTML to contain component name 'Users/Index'. Got: %s", body)
	}
}

func TestRender_JSONResponse(t *testing.T) {
	engine := newTestEngine(t)

	req := httptest.NewRequest("GET", "/users", nil)
	req.Header.Set("X-Inertia", "true")
	rec := httptest.NewRecorder()

	props := Props{
		"users": []map[string]interface{}{
			{"id": 1, "name": "John"},
		},
	}

	err := engine.Render(rec, req, "Users/Index", props)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	contentType := rec.Header().Get("Content-Type")
	if !strings.Contains(contentType, "application/json") {
		t.Errorf("Expected JSON content type, got: %s", contentType)
	}

	var response map[string]interface{}
	err = json.Unmarshal(rec.Body.Bytes(), &response)
	if err != nil {
		t.Fatalf("Failed to parse JSON response: %v", err)
	}

	if response["component"] != "Users/Index" {
		t.Errorf("Expected component to be 'Users/Index', got: %v", response["component"])
	}

	if response["version"] == nil || response["version"] == "" {
		t.Error("Expected version to be present")
	}
}

func TestShare(t *testing.T) {
	engine := newTestEngine(t)

	engine.Share("app_name", "Test App")
	engine.Share("user", map[string]interface{}{
		"id":   123,
		"name": "John",
	})

	req := httptest.NewRequest("GET", "/dashboard", nil)
	req.Header.Set("X-Inertia", "true")
	rec := httptest.NewRecorder()

	err := engine.Render(rec, req, "Dashboard", Props{
		"stats": "some stats",
	})
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	var response map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &response)

	props := response["props"].(map[string]interface{})

	if props["app_name"] != "Test App" {
		t.Error("Expected shared app_name to be included")
	}

	if user, ok := props["user"].(map[string]interface{}); !ok {
		t.Error("Expected shared user to be included")
	} else {
		if user["name"] != "John" {
			t.Error("Expected user name to be 'John'")
		}
	}

	if props["stats"] != "some stats" {
		t.Error("Expected component stats to be included")
	}
}

func TestWithErrors(t *testing.T) {
	engine := newTestEngine(t)

	req := httptest.NewRequest("POST", "/users", nil)
	req.Header.Set("X-Inertia", "true")
	rec := httptest.NewRecorder()

	errors := map[string]interface{}{
		"email": "Email is required",
		"name":  "Name is too short",
	}

	renderProps := Props{
		"form":   "empty",
		"errors": errors,
	}

	err := engine.Render(rec, req, "Users/Create", renderProps)
	if err != nil {
		t.Fatalf("Render with errors failed: %v", err)
	}

	var response map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &response)

	props := response["props"].(map[string]interface{})

	if props["form"] != "empty" {
		t.Error("Expected form prop to be included")
	}

	if props["errors"] == nil {
		t.Error("Expected errors prop to be included")
	}
}

func TestOptionalAndAlwaysProps(t *testing.T) {
	engine := newTestEngine(t)

	req := httptest.NewRequest("GET", "/users", nil)
	req.Header.Set("X-Inertia", "true")
	rec := httptest.NewRecorder()

	props := Props{
		"required": "always included",
		"always":   Always("always included even on partial"),
	}

	err := engine.Render(rec, req, "Users/Index", props)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	var response map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &response)

	responseProps := response["props"].(map[string]interface{})

	if responseProps["required"] != "always included" {
		t.Error("Expected required prop to be included")
	}

	if responseProps["always"] != "always included even on partial" {
		t.Error("Expected always prop to be included")
	}
}

func TestLazyProp(t *testing.T) {
	engine := newTestEngine(t)

	req := httptest.NewRequest("GET", "/users", nil)
	req.Header.Set("X-Inertia", "true")
	rec := httptest.NewRecorder()

	props := Props{
		"lazy": LazyProp("lazy value"),
	}

	err := engine.Render(rec, req, "Users/Index", props)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	// LazyProp should not be included on initial load
	var response map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &response)

	responseProps := response["props"].(map[string]interface{})

	// Lazy props are excluded on initial load
	if _, ok := responseProps["lazy"]; ok {
		t.Error("Lazy prop should not be included on initial load")
	}
}

func TestSimpleFlashProvider(t *testing.T) {
	provider := NewSimpleFlashProvider()

	provider.Set("success", "Operation successful")
	provider.Set("warning", "Be careful")

	req := httptest.NewRequest("GET", "/", nil)

	flash, err := provider.GetFlashData(req)
	if err != nil {
		t.Fatalf("GetFlashData failed: %v", err)
	}

	if flash["success"] != "Operation successful" {
		t.Error("Expected success flash message")
	}

	if flash["warning"] != "Be careful" {
		t.Error("Expected warning flash message")
	}

	flash2, _ := provider.GetFlashData(req)
	if len(flash2) != 0 {
		t.Error("Expected flash to be cleared after reading")
	}
}

func TestSimpleValidationProvider(t *testing.T) {
	provider := NewSimpleValidationProvider()

	errors := map[string]interface{}{
		"email": "Invalid email format",
		"age":   "Must be 18 or older",
	}
	provider.Set(errors)

	req := httptest.NewRequest("POST", "/", nil)

	gotErrors, err := provider.GetValidationErrors(req)
	if err != nil {
		t.Fatalf("GetValidationErrors failed: %v", err)
	}

	if gotErrors["email"] != "Invalid email format" {
		t.Error("Expected email error")
	}

	if gotErrors["age"] != "Must be 18 or older" {
		t.Error("Expected age error")
	}

	errors2, _ := provider.GetValidationErrors(req)
	if len(errors2) != 0 {
		t.Error("Expected errors to be cleared after reading")
	}
}

func TestRedirect(t *testing.T) {
	engine := newTestEngine(t)

	req := httptest.NewRequest("POST", "/", nil)
	rec := httptest.NewRecorder()

	engine.Redirect(rec, req, "/dashboard")

	if rec.Code != 303 {
		t.Errorf("Expected status 303, got %d", rec.Code)
	}

	location := rec.Header().Get("Location")
	if location != "/dashboard" {
		t.Errorf("Expected Location /dashboard, got %s", location)
	}
}

func TestLocation_InertiaRequest(t *testing.T) {
	engine := newTestEngine(t)

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Inertia", "true")
	rec := httptest.NewRecorder()

	engine.Location(rec, req, "/external")

	if rec.Code != 409 {
		t.Errorf("Expected status 409, got %d", rec.Code)
	}

	location := rec.Header().Get("X-Inertia-Location")
	if location != "/external" {
		t.Errorf("Expected X-Inertia-Location /external, got %s", location)
	}
}

func TestLocation_NonInertiaRequest(t *testing.T) {
	engine := newTestEngine(t)

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()

	engine.Location(rec, req, "/external")

	if rec.Code != 302 {
		t.Errorf("Expected status 302, got %d", rec.Code)
	}

	location := rec.Header().Get("Location")
	if location != "/external" {
		t.Errorf("Expected Location /external, got %s", location)
	}
}

func TestBack(t *testing.T) {
	engine := newTestEngine(t)

	tests := []struct {
		name             string
		referer          string
		expectedLocation string
		expectedStatus   int
	}{
		{
			name:             "Back with referer",
			referer:          "/previous-page",
			expectedLocation: "/previous-page",
			expectedStatus:   303,
		},
		{
			name:             "Back without referer",
			referer:          "",
			expectedLocation: "/",
			expectedStatus:   303,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/submit", nil)
			if tt.referer != "" {
				req.Header.Set("Referer", tt.referer)
			}
			rec := httptest.NewRecorder()

			engine.Back(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, rec.Code)
			}

			location := rec.Header().Get("Location")
			if location != tt.expectedLocation {
				t.Errorf("Expected Location header %s, got %s", tt.expectedLocation, location)
			}
		})
	}
}

func TestLoadTemplateFromFile(t *testing.T) {
	// This test would need a temporary file to be complete
	// For now, we just verify the function exists and can be called
	_, err := LoadTemplateFromFile("/nonexistent/file.html")
	if err == nil {
		t.Error("Expected error for nonexistent file")
	}
}

func TestMiddleware(t *testing.T) {
	engine := newTestEngine(t)

	mw := engine.Middleware()
	if mw == nil {
		t.Error("Expected middleware to be returned")
	}
}

func TestSetSharePropsFunc(t *testing.T) {
	engine := newTestEngine(t)

	called := false
	engine.SetSharePropsFunc(func(r *http.Request) (Props, error) {
		called = true
		return Props{
			"auth": map[string]string{"user": "Ali"},
		}, nil
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Inertia", "true")
	rec := httptest.NewRecorder()

	engine.Render(rec, req, "Test", Props{})

	if !called {
		t.Error("Expected SharePropsFunc to be called")
	}
}

func TestShareFunc(t *testing.T) {
	engine := newTestEngine(t)

	engine.ShareFunc("path", func(r *http.Request) (interface{}, error) {
		return r.URL.Path, nil
	})

	req := httptest.NewRequest("GET", "/test-path", nil)
	req.Header.Set("X-Inertia", "true")
	rec := httptest.NewRecorder()

	engine.Render(rec, req, "Test", Props{})

	var response map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &response)

	props := response["props"].(map[string]interface{})

	if props["path"] != "/test-path" {
		t.Errorf("Expected path /test-path, got %v", props["path"])
	}
}

func TestShareMultiple(t *testing.T) {
	engine := newTestEngine(t)

	engine.ShareMultiple(Props{
		"a": 1,
		"b": 2,
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Inertia", "true")
	rec := httptest.NewRecorder()

	engine.Render(rec, req, "Test", Props{})

	var response map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &response)

	props := response["props"].(map[string]interface{})

	if props["a"] != float64(1) {
		t.Errorf("Expected a=1, got %v", props["a"])
	}
	if props["b"] != float64(2) {
		t.Errorf("Expected b=2, got %v", props["b"])
	}
}

func TestNewEngine_DefaultVersion(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
	}{
		{
			name: "uses default version when empty",
			config: Config{
				RootTemplate: `<html><body>{{ .inertia }}</body></html>`,
				Version:      "",
			},
			wantErr: false,
		},
		{
			name: "uses provided version",
			config: Config{
				RootTemplate: `<html><body>{{ .inertia }}</body></html>`,
				Version:      "custom-version",
			},
			wantErr: false,
		},
		{
			name:    "uses all defaults",
			config:  Config{},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewEngine(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewEngine() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRender_WithVariousProps(t *testing.T) {
	tests := []struct {
		name      string
		component string
		props     []Props
		wantProps bool
	}{
		{
			name:      "renders with nil props slice",
			component: "Test/Component",
			props:     nil,
			wantProps: false,
		},
		{
			name:      "renders with empty props slice",
			component: "Test/Component",
			props:     []Props{},
			wantProps: false,
		},
		{
			name:      "renders with nil props in slice",
			component: "Test/Component",
			props:     []Props{nil},
			wantProps: false,
		},
		{
			name:      "renders with empty props map",
			component: "Test/Component",
			props:     []Props{{}},
			wantProps: true,
		},
		{
			name:      "renders with populated props",
			component: "Test/Component",
			props:     []Props{{"key": "value"}},
			wantProps: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := newTestEngine(t)

			req := httptest.NewRequest("GET", "/test", nil)
			req.Header.Set("X-Inertia", "true")
			rec := httptest.NewRecorder()

			var err error
			if tt.props == nil {
				err = engine.Render(rec, req, tt.component)
			} else {
				err = engine.Render(rec, req, tt.component, tt.props...)
			}

			if err != nil {
				t.Fatalf("Render failed: %v", err)
			}

			var response map[string]interface{}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatalf("Failed to parse JSON response: %v", err)
			}

			if response["component"] != tt.component {
				t.Errorf("component = %v, want %v", response["component"], tt.component)
			}
		})
	}
}

func TestLoadTemplateFromFile_ValidFile(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantContent string
		wantErr     bool
	}{
		{
			name:        "loads simple template",
			content:     `<html><body>{{ .inertia }}</body></html>`,
			wantContent: `<html><body>{{ .inertia }}</body></html>`,
			wantErr:     false,
		},
		{
			name:        "loads template with multiple placeholders",
			content:     `<!DOCTYPE html><html><head>{{ .inertiaHead }}</head><body>{{ .inertia }}</body></html>`,
			wantContent: `<!DOCTYPE html><html><head>{{ .inertiaHead }}</head><body>{{ .inertia }}</body></html>`,
			wantErr:     false,
		},
		{
			name:        "loads empty file",
			content:     "",
			wantContent: "",
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp file
			tmpFile, err := os.CreateTemp("", "template-*.html")
			if err != nil {
				t.Fatalf("Failed to create temp file: %v", err)
			}
			defer os.Remove(tmpFile.Name())

			if _, err := tmpFile.WriteString(tt.content); err != nil {
				t.Fatalf("Failed to write to temp file: %v", err)
			}
			tmpFile.Close()

			got, err := LoadTemplateFromFile(tmpFile.Name())
			if (err != nil) != tt.wantErr {
				t.Errorf("LoadTemplateFromFile() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if got != tt.wantContent {
				t.Errorf("LoadTemplateFromFile() = %v, want %v", got, tt.wantContent)
			}
		})
	}
}

func TestLoadTemplateFromFile_Errors(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{
			name:    "returns error for nonexistent file",
			path:    "/nonexistent/path/template.html",
			wantErr: true,
		},
		{
			name:    "returns error for empty path",
			path:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadTemplateFromFile(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("LoadTemplateFromFile() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRender_PackageLevel(t *testing.T) {
	engine := newTestEngine(t)

	ctx, rec := router.NewTestContext("GET", "/dashboard")
	ctx.Request.Header.Set("X-Inertia", "true")
	ctx.SetServices(&app.Services{View: engine})

	err := Render(ctx, "Dashboard", Props{"title": "Home"})
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}
	if response["component"] != "Dashboard" {
		t.Errorf("component = %v, want Dashboard", response["component"])
	}
	props := response["props"].(map[string]interface{})
	if props["title"] != "Home" {
		t.Errorf("props.title = %v, want Home", props["title"])
	}
}

func TestRender_PackageLevel_NoEngine_Panics(t *testing.T) {
	ctx, _ := router.NewTestContext("GET", "/")
	ctx.SetServices(&app.Services{})

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic when view engine is not configured")
		}
	}()

	Render(ctx, "Test")
}

func TestMiddleware_Integration(t *testing.T) {
	tests := []struct {
		name           string
		method         string
		inertiaHeader  bool
		versionMatch   bool
		wantStatusCode int
	}{
		{
			name:           "passes through GET request without Inertia header",
			method:         "GET",
			inertiaHeader:  false,
			versionMatch:   true,
			wantStatusCode: http.StatusOK,
		},
		{
			name:           "passes through GET request with Inertia header",
			method:         "GET",
			inertiaHeader:  true,
			versionMatch:   true,
			wantStatusCode: http.StatusOK,
		},
		{
			name:           "passes through POST request",
			method:         "POST",
			inertiaHeader:  false,
			versionMatch:   true,
			wantStatusCode: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := newTestEngine(t)

			mw := engine.Middleware()

			handler := mw(func(c *router.Context) error {
				c.Response.WriteHeader(http.StatusOK)
				c.Response.Write([]byte("OK"))
				return nil
			})

			req := httptest.NewRequest(tt.method, "/test", nil)
			if tt.inertiaHeader {
				req.Header.Set("X-Inertia", "true")
				if tt.versionMatch {
					req.Header.Set("X-Inertia-Version", "1.0")
				}
			}
			rec := httptest.NewRecorder()

			// Create a router.Context and call the handler
			ctx := router.NewContext(rec, req)
			handler(ctx)

			if rec.Code != tt.wantStatusCode {
				t.Errorf("status code = %d, want %d", rec.Code, tt.wantStatusCode)
			}
		})
	}
}

func TestRedirect_TableDriven(t *testing.T) {
	tests := []struct {
		name           string
		method         string
		url            string
		wantStatusCode int
		wantLocation   string
	}{
		{
			name:           "redirects POST request to dashboard",
			method:         "POST",
			url:            "/dashboard",
			wantStatusCode: http.StatusSeeOther,
			wantLocation:   "/dashboard",
		},
		{
			name:           "redirects GET request to home",
			method:         "GET",
			url:            "/",
			wantStatusCode: http.StatusSeeOther,
			wantLocation:   "/",
		},
		{
			name:           "redirects to nested path",
			method:         "POST",
			url:            "/users/123/profile",
			wantStatusCode: http.StatusSeeOther,
			wantLocation:   "/users/123/profile",
		},
		{
			name:           "redirects to path with query params",
			method:         "POST",
			url:            "/search?q=test",
			wantStatusCode: http.StatusSeeOther,
			wantLocation:   "/search?q=test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := newTestEngine(t)

			req := httptest.NewRequest(tt.method, "/submit", nil)
			rec := httptest.NewRecorder()

			engine.Redirect(rec, req, tt.url)

			if rec.Code != tt.wantStatusCode {
				t.Errorf("status code = %d, want %d", rec.Code, tt.wantStatusCode)
			}

			location := rec.Header().Get("Location")
			if location != tt.wantLocation {
				t.Errorf("Location header = %s, want %s", location, tt.wantLocation)
			}
		})
	}
}
