package view

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/pkg/bond"
)

func resetBond() {
	// Use bond's reset function for testing
	bond.ResetForTesting()
}

func TestInitialize(t *testing.T) {
	resetBond()
	defer resetBond()

	config := Config{
		RootTemplate: `<html><body>{{ .inertia }}</body></html>`,
		Version:      "test-version",
	}

	err := Initialize(config)
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	// Should not panic - instance is initialized
	_ = bond.Get()
}

func TestInitialize_DefaultTemplate(t *testing.T) {
	resetBond()
	defer resetBond()

	config := Config{
		Version: "1.0",
	}

	err := Initialize(config)
	if err != nil {
		t.Fatalf("Initialize failed with default template: %v", err)
	}
}

func TestRender_HTMLResponse(t *testing.T) {
	resetBond()
	defer resetBond()

	Initialize(Config{
		RootTemplate: `<html><body>{{ .inertia }}</body></html>`,
		Version:      "1.0",
	})

	req := httptest.NewRequest("GET", "/users", nil)
	rec := httptest.NewRecorder()

	props := Props{
		"users": []map[string]interface{}{
			{"id": 1, "name": "John"},
			{"id": 2, "name": "Jane"},
		},
	}

	err := Render(rec, req, "Users/Index", props)
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
	resetBond()
	defer resetBond()

	Initialize(Config{
		RootTemplate: defaultTemplate,
		Version:      "1.0",
	})

	req := httptest.NewRequest("GET", "/users", nil)
	req.Header.Set("X-Inertia", "true")
	rec := httptest.NewRecorder()

	props := Props{
		"users": []map[string]interface{}{
			{"id": 1, "name": "John"},
		},
	}

	err := Render(rec, req, "Users/Index", props)
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
	resetBond()
	defer resetBond()

	Initialize(Config{
		RootTemplate: defaultTemplate,
		Version:      "1.0",
	})

	Share("app_name", "Test App")
	Share("user", map[string]interface{}{
		"id":   123,
		"name": "John",
	})

	req := httptest.NewRequest("GET", "/dashboard", nil)
	req.Header.Set("X-Inertia", "true")
	rec := httptest.NewRecorder()

	err := Render(rec, req, "Dashboard", Props{
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
	resetBond()
	defer resetBond()

	Initialize(Config{
		RootTemplate: defaultTemplate,
		Version:      "1.0",
	})

	req := httptest.NewRequest("POST", "/users", nil)
	req.Header.Set("X-Inertia", "true")
	rec := httptest.NewRecorder()

	errors := map[string]interface{}{
		"email": "Email is required",
		"name":  "Name is too short",
	}

	err := WithErrors(rec, req, "Users/Create", Props{
		"form": "empty",
	}, errors)

	if err != nil {
		t.Fatalf("WithErrors failed: %v", err)
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
	resetBond()
	defer resetBond()

	Initialize(Config{
		RootTemplate: defaultTemplate,
		Version:      "1.0",
	})

	req := httptest.NewRequest("GET", "/users", nil)
	req.Header.Set("X-Inertia", "true")
	rec := httptest.NewRecorder()

	props := Props{
		"required": "always included",
		"always":   Always("always included even on partial"),
	}

	err := Render(rec, req, "Users/Index", props)
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
	resetBond()
	defer resetBond()

	Initialize(Config{
		RootTemplate: defaultTemplate,
		Version:      "1.0",
	})

	req := httptest.NewRequest("GET", "/users", nil)
	req.Header.Set("X-Inertia", "true")
	rec := httptest.NewRecorder()

	props := Props{
		"lazy": LazyProp("lazy value"),
	}

	err := Render(rec, req, "Users/Index", props)
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
	resetBond()
	defer resetBond()

	Initialize(Config{
		RootTemplate: defaultTemplate,
		Version:      "1.0",
	})

	req := httptest.NewRequest("POST", "/", nil)
	rec := httptest.NewRecorder()

	Redirect(rec, req, "/dashboard")

	if rec.Code != 303 {
		t.Errorf("Expected status 303, got %d", rec.Code)
	}

	location := rec.Header().Get("Location")
	if location != "/dashboard" {
		t.Errorf("Expected Location /dashboard, got %s", location)
	}
}

func TestLocation_InertiaRequest(t *testing.T) {
	resetBond()
	defer resetBond()

	Initialize(Config{
		RootTemplate: defaultTemplate,
		Version:      "1.0",
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Inertia", "true")
	rec := httptest.NewRecorder()

	Location(rec, req, "/external")

	if rec.Code != 409 {
		t.Errorf("Expected status 409, got %d", rec.Code)
	}

	location := rec.Header().Get("X-Inertia-Location")
	if location != "/external" {
		t.Errorf("Expected X-Inertia-Location /external, got %s", location)
	}
}

func TestLocation_NonInertiaRequest(t *testing.T) {
	resetBond()
	defer resetBond()

	Initialize(Config{
		RootTemplate: defaultTemplate,
		Version:      "1.0",
	})

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()

	Location(rec, req, "/external")

	if rec.Code != 302 {
		t.Errorf("Expected status 302, got %d", rec.Code)
	}

	location := rec.Header().Get("Location")
	if location != "/external" {
		t.Errorf("Expected Location /external, got %s", location)
	}
}

func TestBack(t *testing.T) {
	resetBond()
	defer resetBond()

	Initialize(Config{
		RootTemplate: defaultTemplate,
		Version:      "1.0",
	})

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

			Back(rec, req)

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
	resetBond()
	defer resetBond()

	Initialize(Config{
		RootTemplate: defaultTemplate,
		Version:      "1.0",
	})

	mw := Middleware()
	if mw == nil {
		t.Error("Expected middleware to be returned")
	}
}

func TestSetSharePropsFunc(t *testing.T) {
	resetBond()
	defer resetBond()

	Initialize(Config{
		RootTemplate: defaultTemplate,
		Version:      "1.0",
	})

	called := false
	SetSharePropsFunc(func(r *http.Request) (Props, error) {
		called = true
		return Props{
			"auth": map[string]string{"user": "Ali"},
		}, nil
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Inertia", "true")
	rec := httptest.NewRecorder()

	Render(rec, req, "Test", Props{})

	if !called {
		t.Error("Expected SharePropsFunc to be called")
	}
}

func TestShareFunc(t *testing.T) {
	resetBond()
	defer resetBond()

	Initialize(Config{
		RootTemplate: defaultTemplate,
		Version:      "1.0",
	})

	ShareFunc("path", func(r *http.Request) (interface{}, error) {
		return r.URL.Path, nil
	})

	req := httptest.NewRequest("GET", "/test-path", nil)
	req.Header.Set("X-Inertia", "true")
	rec := httptest.NewRecorder()

	Render(rec, req, "Test", Props{})

	var response map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &response)

	props := response["props"].(map[string]interface{})

	if props["path"] != "/test-path" {
		t.Errorf("Expected path /test-path, got %v", props["path"])
	}
}

func TestShareMultiple(t *testing.T) {
	resetBond()
	defer resetBond()

	Initialize(Config{
		RootTemplate: defaultTemplate,
		Version:      "1.0",
	})

	ShareMultiple(Props{
		"a": 1,
		"b": 2,
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Inertia", "true")
	rec := httptest.NewRecorder()

	Render(rec, req, "Test", Props{})

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
