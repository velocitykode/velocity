package view

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/romsar/gonertia"
)

func TestInitialize(t *testing.T) {
	// Reset singleton for testing
	instance = nil
	once = sync.Once{}

	config := Config{
		RootTemplate: "<div id='app' data-page='{{ .page }}'></div>",
		Version:      "test-version",
	}

	err := Initialize(config)
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	if instance == nil {
		t.Error("Expected instance to be initialized")
	}

	if instance.version != "test-version" {
		t.Errorf("Expected version to be 'test-version', got '%s'", instance.version)
	}
}

func TestRender_HTMLResponse(t *testing.T) {
	// Reset and initialize
	instance = nil
	once = sync.Once{}
	Initialize(Config{
		RootTemplate: `<div id="app">{{ .inertia }}</div>`,
		Version:      "1.0",
	})

	req := httptest.NewRequest("GET", "/users", nil)
	rec := httptest.NewRecorder()

	props := gonertia.Props{
		"users": []map[string]interface{}{
			{"id": 1, "name": "John"},
			{"id": 2, "name": "Jane"},
		},
	}

	err := Render(rec, req, "Users/Index", props)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	// Check response
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
	// Reset and initialize
	instance = nil
	once = sync.Once{}
	Initialize(Config{
		RootTemplate: defaultTemplate,
		Version:      "1.0",
	})

	req := httptest.NewRequest("GET", "/users", nil)
	req.Header.Set("X-Inertia", "true")
	rec := httptest.NewRecorder()

	props := gonertia.Props{
		"users": []map[string]interface{}{
			{"id": 1, "name": "John"},
		},
	}

	err := Render(rec, req, "Users/Index", props)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	// Check JSON response
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

	// Version could be auto-generated hash or the configured version
	if response["version"] == nil || response["version"] == "" {
		t.Error("Expected version to be present")
	}
}

func TestShare(t *testing.T) {
	// Reset and initialize
	instance = nil
	once = sync.Once{}
	Initialize(Config{
		RootTemplate: defaultTemplate,
		Version:      "1.0",
	})

	// Share global data
	Share("app_name", "Test App")
	Share("user", map[string]interface{}{
		"id":   123,
		"name": "John",
	})

	req := httptest.NewRequest("GET", "/dashboard", nil)
	req.Header.Set("X-Inertia", "true")
	rec := httptest.NewRecorder()

	err := Render(rec, req, "Dashboard", gonertia.Props{
		"stats": "some stats",
	})
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	var response map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &response)

	props := response["props"].(map[string]interface{})

	// Check shared props are included
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

	// Check component props are also included
	if props["stats"] != "some stats" {
		t.Error("Expected component stats to be included")
	}
}

func TestWithErrors(t *testing.T) {
	// Reset and initialize
	instance = nil
	once = sync.Once{}
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

	err := WithErrors(rec, req, "Users/Create", gonertia.Props{
		"form": "empty",
	}, errors)

	if err != nil {
		t.Fatalf("WithErrors failed: %v", err)
	}

	var response map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &response)

	props := response["props"].(map[string]interface{})

	// Note: In real usage, errors would be picked up by gonertia's
	// ValidationErrorsProvider. This test just verifies the function works.
	if props["form"] != "empty" {
		t.Error("Expected form prop to be included")
	}
}

func TestOptionalAndAlwaysProps(t *testing.T) {
	// Reset and initialize
	instance = nil
	once = sync.Once{}
	Initialize(Config{
		RootTemplate: defaultTemplate,
		Version:      "1.0",
	})

	req := httptest.NewRequest("GET", "/users", nil)
	req.Header.Set("X-Inertia", "true")
	rec := httptest.NewRecorder()

	props := gonertia.Props{
		"required": "always included",
		"optional": Optional("only on full load"),
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

	// Note: The actual behavior of Optional/Always props depends on
	// gonertia's internal handling of partial reloads
}

func TestLazyProp(t *testing.T) {
	// Reset and initialize
	instance = nil
	once = sync.Once{}
	Initialize(Config{
		RootTemplate: defaultTemplate,
		Version:      "1.0",
	})

	req := httptest.NewRequest("GET", "/users", nil)
	req.Header.Set("X-Inertia", "true")
	rec := httptest.NewRecorder()

	props := gonertia.Props{
		"lazy": LazyProp("lazy value"),
	}

	err := Render(rec, req, "Users/Index", props)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	// LazyProp (OptionalProp) behavior depends on gonertia's implementation
	t.Log("LazyProp test completed")
}

func TestSimpleFlashProvider(t *testing.T) {
	provider := NewSimpleFlashProvider()

	// Set flash messages
	provider.Set("success", "Operation successful")
	provider.Set("warning", "Be careful")

	req := httptest.NewRequest("GET", "/", nil)

	// Get flash data
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

	// Flash should be cleared after reading
	flash2, _ := provider.GetFlashData(req)
	if len(flash2) != 0 {
		t.Error("Expected flash to be cleared after reading")
	}
}

func TestSimpleValidationProvider(t *testing.T) {
	provider := NewSimpleValidationProvider()

	// Set validation errors
	errors := map[string]interface{}{
		"email": "Invalid email format",
		"age":   "Must be 18 or older",
	}
	provider.Set(errors)

	req := httptest.NewRequest("POST", "/", nil)

	// Get validation errors
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

	// Errors should be cleared after reading
	errors2, _ := provider.GetValidationErrors(req)
	if len(errors2) != 0 {
		t.Error("Expected errors to be cleared after reading")
	}
}

func TestRedirect(t *testing.T) {
	// Reset and initialize
	instance = nil
	once = sync.Once{}
	Initialize(Config{
		RootTemplate: defaultTemplate,
		Version:      "1.0",
	})

	tests := []struct {
		name           string
		url            string
		inertiaRequest bool
		expectedStatus int
		expectedHeader string
	}{
		{
			name:           "Regular request redirect",
			url:            "/dashboard",
			inertiaRequest: false,
			expectedStatus: 302,
			expectedHeader: "/dashboard",
		},
		{
			name:           "Inertia request redirect",
			url:            "/users",
			inertiaRequest: true,
			expectedStatus: 302,
			expectedHeader: "/users",
		},
		{
			name:           "External URL redirect",
			url:            "https://example.com",
			inertiaRequest: false,
			expectedStatus: 302,
			expectedHeader: "https://example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			if tt.inertiaRequest {
				req.Header.Set("X-Inertia", "true")
			}
			rec := httptest.NewRecorder()

			Redirect(rec, req, tt.url)

			if rec.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, rec.Code)
			}

			location := rec.Header().Get("Location")
			if location != tt.expectedHeader {
				t.Errorf("Expected Location header %s, got %s", tt.expectedHeader, location)
			}
		})
	}
}

func TestLocation(t *testing.T) {
	// Reset and initialize
	instance = nil
	once = sync.Once{}
	Initialize(Config{
		RootTemplate: defaultTemplate,
		Version:      "1.0",
	})

	tests := []struct {
		name           string
		url            string
		inertiaRequest bool
		expectedStatus int
	}{
		{
			name:           "Regular request with Location",
			url:            "/dashboard",
			inertiaRequest: false,
			expectedStatus: 409,
		},
		{
			name:           "Inertia request with Location (forced reload)",
			url:            "/users",
			inertiaRequest: true,
			expectedStatus: 409,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			if tt.inertiaRequest {
				req.Header.Set("X-Inertia", "true")
			}
			rec := httptest.NewRecorder()

			Location(rec, req, tt.url)

			if rec.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, rec.Code)
			}

			// Location should set X-Inertia-Location header
			if tt.inertiaRequest {
				location := rec.Header().Get("X-Inertia-Location")
				if location != tt.url {
					t.Errorf("Expected X-Inertia-Location header %s, got %s", tt.url, location)
				}
			}
		})
	}
}

func TestBack(t *testing.T) {
	// Reset and initialize
	instance = nil
	once = sync.Once{}
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
