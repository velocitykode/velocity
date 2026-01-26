package view

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/pkg/bond"
)

func TestWithFlash(t *testing.T) {
	tests := []struct {
		name      string
		component string
		props     Props
		flash     map[string]interface{}
		wantFlash map[string]interface{}
	}{
		{
			name:      "renders with flash messages and existing props",
			component: "Dashboard",
			props:     Props{"data": "value"},
			flash:     map[string]interface{}{"success": "Operation completed"},
			wantFlash: map[string]interface{}{"success": "Operation completed"},
		},
		{
			name:      "renders with flash messages and nil props",
			component: "Dashboard",
			props:     nil,
			flash:     map[string]interface{}{"error": "Something went wrong"},
			wantFlash: map[string]interface{}{"error": "Something went wrong"},
		},
		{
			name:      "renders with multiple flash messages",
			component: "Settings",
			props:     Props{},
			flash: map[string]interface{}{
				"success": "Saved",
				"info":    "Check your email",
			},
			wantFlash: map[string]interface{}{
				"success": "Saved",
				"info":    "Check your email",
			},
		},
		{
			name:      "renders with empty flash map",
			component: "Home",
			props:     Props{"user": "test"},
			flash:     map[string]interface{}{},
			wantFlash: map[string]interface{}{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bond.ResetForTesting()
			defer bond.ResetForTesting()

			Initialize(Config{
				RootTemplate: defaultTemplate,
				Version:      "1.0",
			})

			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("X-Inertia", "true")
			rec := httptest.NewRecorder()

			err := WithFlash(rec, req, tt.component, tt.props, tt.flash)
			if err != nil {
				t.Fatalf("WithFlash failed: %v", err)
			}

			var response map[string]interface{}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatalf("Failed to parse JSON response: %v", err)
			}

			props := response["props"].(map[string]interface{})
			flash, ok := props["flash"].(map[string]interface{})
			if !ok {
				t.Fatal("Expected flash to be present in props")
			}

			for key, want := range tt.wantFlash {
				if flash[key] != want {
					t.Errorf("flash[%s] = %v, want %v", key, flash[key], want)
				}
			}
		})
	}
}

func TestSuccess(t *testing.T) {
	tests := []struct {
		name           string
		message        string
		url            string
		wantStatusCode int
		wantLocation   string
	}{
		{
			name:           "redirects to dashboard with success message",
			message:        "User created successfully",
			url:            "/dashboard",
			wantStatusCode: http.StatusSeeOther,
			wantLocation:   "/dashboard",
		},
		{
			name:           "redirects to root path",
			message:        "Welcome back",
			url:            "/",
			wantStatusCode: http.StatusSeeOther,
			wantLocation:   "/",
		},
		{
			name:           "redirects to nested path",
			message:        "Settings saved",
			url:            "/users/123/settings",
			wantStatusCode: http.StatusSeeOther,
			wantLocation:   "/users/123/settings",
		},
		{
			name:           "redirects with empty message",
			message:        "",
			url:            "/home",
			wantStatusCode: http.StatusSeeOther,
			wantLocation:   "/home",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/submit", nil)
			rec := httptest.NewRecorder()

			Success(rec, req, tt.message, tt.url)

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

func TestError(t *testing.T) {
	tests := []struct {
		name           string
		message        string
		url            string
		wantStatusCode int
		wantLocation   string
	}{
		{
			name:           "redirects with error message",
			message:        "Invalid credentials",
			url:            "/login",
			wantStatusCode: http.StatusSeeOther,
			wantLocation:   "/login",
		},
		{
			name:           "redirects to previous page",
			message:        "Permission denied",
			url:            "/admin",
			wantStatusCode: http.StatusSeeOther,
			wantLocation:   "/admin",
		},
		{
			name:           "redirects with empty message",
			message:        "",
			url:            "/error",
			wantStatusCode: http.StatusSeeOther,
			wantLocation:   "/error",
		},
		{
			name:           "redirects to complex path",
			message:        "Not found",
			url:            "/api/v1/resources/404",
			wantStatusCode: http.StatusSeeOther,
			wantLocation:   "/api/v1/resources/404",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/submit", nil)
			rec := httptest.NewRecorder()

			Error(rec, req, tt.message, tt.url)

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

func TestFormError(t *testing.T) {
	tests := []struct {
		name           string
		referer        string
		errors         map[string]interface{}
		wantStatusCode int
		wantLocation   string
	}{
		{
			name:    "redirects to referer with form errors",
			referer: "/users/create",
			errors: map[string]interface{}{
				"email": "Email is required",
				"name":  "Name is too short",
			},
			wantStatusCode: http.StatusSeeOther,
			wantLocation:   "/users/create",
		},
		{
			name:           "redirects to root without referer",
			referer:        "",
			errors:         map[string]interface{}{"field": "error"},
			wantStatusCode: http.StatusSeeOther,
			wantLocation:   "/",
		},
		{
			name:           "redirects with empty errors",
			referer:        "/form",
			errors:         map[string]interface{}{},
			wantStatusCode: http.StatusSeeOther,
			wantLocation:   "/form",
		},
		{
			name:    "redirects with multiple errors",
			referer: "/register",
			errors: map[string]interface{}{
				"username": "Username taken",
				"password": "Password too weak",
				"email":    "Invalid email format",
			},
			wantStatusCode: http.StatusSeeOther,
			wantLocation:   "/register",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bond.ResetForTesting()
			defer bond.ResetForTesting()

			Initialize(Config{
				RootTemplate: defaultTemplate,
				Version:      "1.0",
			})

			req := httptest.NewRequest("POST", "/submit", nil)
			if tt.referer != "" {
				req.Header.Set("Referer", tt.referer)
			}
			rec := httptest.NewRecorder()

			FormError(rec, req, tt.errors)

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

func TestGetOldInput(t *testing.T) {
	tests := []struct {
		name string
		want map[string]interface{}
	}{
		{
			name: "returns empty map for fresh request",
			want: map[string]interface{}{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := NewSimpleValidationProvider()
			req := httptest.NewRequest("GET", "/", nil)

			got, err := provider.GetOldInput(req)
			if err != nil {
				t.Fatalf("GetOldInput returned error: %v", err)
			}

			if len(got) != len(tt.want) {
				t.Errorf("GetOldInput returned %d items, want %d", len(got), len(tt.want))
			}
		})
	}
}

func TestLazy(t *testing.T) {
	tests := []struct {
		name      string
		fn        func() (any, error)
		wantValue any
		wantErr   bool
	}{
		{
			name: "wraps function and evaluates correctly",
			fn: func() (any, error) {
				return "lazy value", nil
			},
			wantValue: "lazy value",
			wantErr:   false,
		},
		{
			name: "wraps function returning complex type",
			fn: func() (any, error) {
				return map[string]int{"count": 42}, nil
			},
			wantValue: map[string]int{"count": 42},
			wantErr:   false,
		},
		{
			name: "wraps function returning nil",
			fn: func() (any, error) {
				return nil, nil
			},
			wantValue: nil,
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Verify function is not evaluated on creation
			evaluated := false
			wrappedFn := func() (any, error) {
				evaluated = true
				return tt.fn()
			}

			prop := Lazy(wrappedFn)

			if evaluated {
				t.Error("function was evaluated on creation, should be lazy")
			}

			// Verify returns correct bond type
			if _, ok := interface{}(prop).(bond.LazyProp); !ok {
				t.Error("Lazy did not return bond.LazyProp type")
			}

			// Verify Evaluate calls the wrapped function
			got, err := prop.Evaluate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Evaluate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !evaluated {
				t.Error("function was not evaluated when Evaluate() was called")
			}

			// Compare values (handle map comparison)
			switch v := tt.wantValue.(type) {
			case map[string]int:
				gotMap, ok := got.(map[string]int)
				if !ok {
					t.Errorf("Evaluate() returned wrong type, got %T", got)
					return
				}
				for k, wantV := range v {
					if gotMap[k] != wantV {
						t.Errorf("Evaluate()[%s] = %v, want %v", k, gotMap[k], wantV)
					}
				}
			default:
				if got != tt.wantValue {
					t.Errorf("Evaluate() = %v, want %v", got, tt.wantValue)
				}
			}
		})
	}
}

func TestOptional(t *testing.T) {
	tests := []struct {
		name      string
		fn        func() (any, error)
		wantValue any
		wantErr   bool
	}{
		{
			name: "wraps function and evaluates correctly",
			fn: func() (any, error) {
				return "optional value", nil
			},
			wantValue: "optional value",
			wantErr:   false,
		},
		{
			name: "wraps function returning slice",
			fn: func() (any, error) {
				return []string{"a", "b", "c"}, nil
			},
			wantValue: []string{"a", "b", "c"},
			wantErr:   false,
		},
		{
			name: "wraps function returning integer",
			fn: func() (any, error) {
				return 123, nil
			},
			wantValue: 123,
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Verify function is not evaluated on creation
			evaluated := false
			wrappedFn := func() (any, error) {
				evaluated = true
				return tt.fn()
			}

			prop := Optional(wrappedFn)

			if evaluated {
				t.Error("function was evaluated on creation, should be lazy")
			}

			// Verify returns correct bond type
			if _, ok := interface{}(prop).(bond.OptionalProp); !ok {
				t.Error("Optional did not return bond.OptionalProp type")
			}

			// Verify Evaluate calls the wrapped function
			got, err := prop.Evaluate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Evaluate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !evaluated {
				t.Error("function was not evaluated when Evaluate() was called")
			}

			// Compare values (handle slice comparison)
			switch v := tt.wantValue.(type) {
			case []string:
				gotSlice, ok := got.([]string)
				if !ok {
					t.Errorf("Evaluate() returned wrong type, got %T", got)
					return
				}
				if len(gotSlice) != len(v) {
					t.Errorf("Evaluate() returned slice of length %d, want %d", len(gotSlice), len(v))
					return
				}
				for i := range v {
					if gotSlice[i] != v[i] {
						t.Errorf("Evaluate()[%d] = %v, want %v", i, gotSlice[i], v[i])
					}
				}
			default:
				if got != tt.wantValue {
					t.Errorf("Evaluate() = %v, want %v", got, tt.wantValue)
				}
			}
		})
	}
}

func TestDefer(t *testing.T) {
	tests := []struct {
		name      string
		fn        func() (any, error)
		group     []string
		wantValue any
		wantGroup string
		wantErr   bool
	}{
		{
			name: "uses default group when none provided",
			fn: func() (any, error) {
				return "deferred value", nil
			},
			group:     nil,
			wantValue: "deferred value",
			wantGroup: "default",
			wantErr:   false,
		},
		{
			name: "uses custom group when provided",
			fn: func() (any, error) {
				return "custom group value", nil
			},
			group:     []string{"analytics"},
			wantValue: "custom group value",
			wantGroup: "analytics",
			wantErr:   false,
		},
		{
			name: "uses default group when empty string provided",
			fn: func() (any, error) {
				return "empty group value", nil
			},
			group:     []string{""},
			wantValue: "empty group value",
			wantGroup: "default",
			wantErr:   false,
		},
		{
			name: "returns complex type",
			fn: func() (any, error) {
				return struct{ Name string }{Name: "test"}, nil
			},
			group:     []string{"data"},
			wantValue: struct{ Name string }{Name: "test"},
			wantGroup: "data",
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Verify function is not evaluated on creation
			evaluated := false
			wrappedFn := func() (any, error) {
				evaluated = true
				return tt.fn()
			}

			var prop bond.DeferredProp
			if tt.group == nil {
				prop = Defer(wrappedFn)
			} else {
				prop = Defer(wrappedFn, tt.group...)
			}

			if evaluated {
				t.Error("function was evaluated on creation, should be deferred")
			}

			// Verify returns correct bond type
			if _, ok := interface{}(prop).(bond.DeferredProp); !ok {
				t.Error("Defer did not return bond.DeferredProp type")
			}

			// Verify group is set correctly
			if prop.Group() != tt.wantGroup {
				t.Errorf("Group() = %s, want %s", prop.Group(), tt.wantGroup)
			}

			// Verify Evaluate calls the wrapped function
			got, err := prop.Evaluate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Evaluate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !evaluated {
				t.Error("function was not evaluated when Evaluate() was called")
			}

			// Compare values (handle struct comparison)
			switch v := tt.wantValue.(type) {
			case struct{ Name string }:
				gotStruct, ok := got.(struct{ Name string })
				if !ok {
					t.Errorf("Evaluate() returned wrong type, got %T", got)
					return
				}
				if gotStruct != v {
					t.Errorf("Evaluate() = %v, want %v", gotStruct, v)
				}
			default:
				if got != tt.wantValue {
					t.Errorf("Evaluate() = %v, want %v", got, tt.wantValue)
				}
			}
		})
	}
}

func TestAlways(t *testing.T) {
	tests := []struct {
		name      string
		value     any
		wantValue any
	}{
		{
			name:      "wraps string value",
			value:     "always string",
			wantValue: "always string",
		},
		{
			name:      "wraps integer value",
			value:     42,
			wantValue: 42,
		},
		{
			name:      "wraps nil value",
			value:     nil,
			wantValue: nil,
		},
		{
			name:      "wraps map value",
			value:     map[string]string{"key": "value"},
			wantValue: map[string]string{"key": "value"},
		},
		{
			name:      "wraps slice value",
			value:     []int{1, 2, 3},
			wantValue: []int{1, 2, 3},
		},
		{
			name:      "wraps boolean value",
			value:     true,
			wantValue: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prop := Always(tt.value)

			// Verify returns correct bond type
			if _, ok := interface{}(prop).(bond.AlwaysProp); !ok {
				t.Error("Always did not return bond.AlwaysProp type")
			}

			// Verify value is accessible
			got := prop.Value()

			// Compare values based on type
			switch v := tt.wantValue.(type) {
			case map[string]string:
				gotMap, ok := got.(map[string]string)
				if !ok {
					t.Errorf("Value() returned wrong type, got %T", got)
					return
				}
				for k, wantV := range v {
					if gotMap[k] != wantV {
						t.Errorf("Value()[%s] = %v, want %v", k, gotMap[k], wantV)
					}
				}
			case []int:
				gotSlice, ok := got.([]int)
				if !ok {
					t.Errorf("Value() returned wrong type, got %T", got)
					return
				}
				if len(gotSlice) != len(v) {
					t.Errorf("Value() returned slice of length %d, want %d", len(gotSlice), len(v))
					return
				}
				for i := range v {
					if gotSlice[i] != v[i] {
						t.Errorf("Value()[%d] = %v, want %v", i, gotSlice[i], v[i])
					}
				}
			default:
				if got != tt.wantValue {
					t.Errorf("Value() = %v, want %v", got, tt.wantValue)
				}
			}
		})
	}
}

func TestSimpleFlashProvider_EdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*SimpleFlashProvider)
		wantFlash map[string]interface{}
	}{
		{
			name:      "returns empty map when no messages set",
			setup:     func(p *SimpleFlashProvider) {},
			wantFlash: map[string]interface{}{},
		},
		{
			name: "overwrites existing message with same key",
			setup: func(p *SimpleFlashProvider) {
				p.Set("success", "First message")
				p.Set("success", "Second message")
			},
			wantFlash: map[string]interface{}{"success": "Second message"},
		},
		{
			name: "handles nil value",
			setup: func(p *SimpleFlashProvider) {
				p.Set("nullable", nil)
			},
			wantFlash: map[string]interface{}{"nullable": nil},
		},
		{
			name: "handles complex value types",
			setup: func(p *SimpleFlashProvider) {
				p.Set("data", map[string]interface{}{"nested": "value"})
			},
			wantFlash: map[string]interface{}{"data": map[string]interface{}{"nested": "value"}},
		},
		{
			name: "handles empty string key",
			setup: func(p *SimpleFlashProvider) {
				p.Set("", "empty key value")
			},
			wantFlash: map[string]interface{}{"": "empty key value"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := NewSimpleFlashProvider()
			tt.setup(provider)

			req := httptest.NewRequest("GET", "/", nil)
			flash, err := provider.GetFlashData(req)
			if err != nil {
				t.Fatalf("GetFlashData returned error: %v", err)
			}

			if len(flash) != len(tt.wantFlash) {
				t.Errorf("GetFlashData returned %d items, want %d", len(flash), len(tt.wantFlash))
			}

			for key, want := range tt.wantFlash {
				got := flash[key]
				// Handle nested map comparison
				if wantMap, ok := want.(map[string]interface{}); ok {
					gotMap, ok := got.(map[string]interface{})
					if !ok {
						t.Errorf("flash[%s] has wrong type %T", key, got)
						continue
					}
					for k, v := range wantMap {
						if gotMap[k] != v {
							t.Errorf("flash[%s][%s] = %v, want %v", key, k, gotMap[k], v)
						}
					}
				} else if got != want {
					t.Errorf("flash[%s] = %v, want %v", key, got, want)
				}
			}
		})
	}
}

func TestSimpleFlashProvider_ClearsAfterRead(t *testing.T) {
	provider := NewSimpleFlashProvider()
	provider.Set("message", "test")

	req := httptest.NewRequest("GET", "/", nil)

	// First read
	flash1, _ := provider.GetFlashData(req)
	if len(flash1) != 1 {
		t.Errorf("First read should have 1 item, got %d", len(flash1))
	}

	// Second read should be empty
	flash2, _ := provider.GetFlashData(req)
	if len(flash2) != 0 {
		t.Errorf("Second read should be empty, got %d items", len(flash2))
	}

	// Third read should still be empty
	flash3, _ := provider.GetFlashData(req)
	if len(flash3) != 0 {
		t.Errorf("Third read should be empty, got %d items", len(flash3))
	}
}

func TestSimpleValidationProvider_EdgeCases(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(*SimpleValidationProvider)
		wantErrors map[string]interface{}
	}{
		{
			name:       "returns empty map when no errors set",
			setup:      func(p *SimpleValidationProvider) {},
			wantErrors: map[string]interface{}{},
		},
		{
			name: "overwrites all errors on Set",
			setup: func(p *SimpleValidationProvider) {
				p.Set(map[string]interface{}{"email": "first error"})
				p.Set(map[string]interface{}{"name": "second error"})
			},
			wantErrors: map[string]interface{}{"name": "second error"},
		},
		{
			name: "handles nil error map",
			setup: func(p *SimpleValidationProvider) {
				p.Set(nil)
			},
			wantErrors: nil,
		},
		{
			name: "handles empty error map",
			setup: func(p *SimpleValidationProvider) {
				p.Set(map[string]interface{}{})
			},
			wantErrors: map[string]interface{}{},
		},
		{
			name: "handles complex error values",
			setup: func(p *SimpleValidationProvider) {
				p.Set(map[string]interface{}{
					"email": []string{"required", "invalid format"},
				})
			},
			wantErrors: map[string]interface{}{
				"email": []string{"required", "invalid format"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := NewSimpleValidationProvider()
			tt.setup(provider)

			req := httptest.NewRequest("POST", "/", nil)
			errors, err := provider.GetValidationErrors(req)
			if err != nil {
				t.Fatalf("GetValidationErrors returned error: %v", err)
			}

			if tt.wantErrors == nil {
				if errors != nil && len(errors) > 0 {
					t.Errorf("GetValidationErrors returned %v, want nil/empty", errors)
				}
				return
			}

			if len(errors) != len(tt.wantErrors) {
				t.Errorf("GetValidationErrors returned %d items, want %d", len(errors), len(tt.wantErrors))
			}

			for key, want := range tt.wantErrors {
				got := errors[key]
				// Handle slice comparison
				if wantSlice, ok := want.([]string); ok {
					gotSlice, ok := got.([]string)
					if !ok {
						t.Errorf("errors[%s] has wrong type %T", key, got)
						continue
					}
					if len(gotSlice) != len(wantSlice) {
						t.Errorf("errors[%s] has %d items, want %d", key, len(gotSlice), len(wantSlice))
						continue
					}
					for i, v := range wantSlice {
						if gotSlice[i] != v {
							t.Errorf("errors[%s][%d] = %v, want %v", key, i, gotSlice[i], v)
						}
					}
				} else if got != want {
					t.Errorf("errors[%s] = %v, want %v", key, got, want)
				}
			}
		})
	}
}

func TestSimpleValidationProvider_ClearsAfterRead(t *testing.T) {
	provider := NewSimpleValidationProvider()
	provider.Set(map[string]interface{}{"email": "required"})

	req := httptest.NewRequest("POST", "/", nil)

	// First read
	errors1, _ := provider.GetValidationErrors(req)
	if len(errors1) != 1 {
		t.Errorf("First read should have 1 item, got %d", len(errors1))
	}

	// Second read should be empty
	errors2, _ := provider.GetValidationErrors(req)
	if len(errors2) != 0 {
		t.Errorf("Second read should be empty, got %d items", len(errors2))
	}
}

func TestWithErrors_EdgeCases(t *testing.T) {
	tests := []struct {
		name       string
		component  string
		props      Props
		errors     map[string]interface{}
		wantErrors map[string]interface{}
	}{
		{
			name:       "handles nil props with errors",
			component:  "Form",
			props:      nil,
			errors:     map[string]interface{}{"field": "error"},
			wantErrors: map[string]interface{}{"field": "error"},
		},
		{
			name:       "handles empty props with errors",
			component:  "Form",
			props:      Props{},
			errors:     map[string]interface{}{"email": "invalid"},
			wantErrors: map[string]interface{}{"email": "invalid"},
		},
		{
			name:       "handles empty errors map",
			component:  "Form",
			props:      Props{"data": "value"},
			errors:     map[string]interface{}{},
			wantErrors: map[string]interface{}{},
		},
		{
			name:       "handles nil errors map",
			component:  "Form",
			props:      Props{"data": "value"},
			errors:     nil,
			wantErrors: nil,
		},
		{
			name:       "merges errors with existing props",
			component:  "UserForm",
			props:      Props{"user": "John", "age": 30},
			errors:     map[string]interface{}{"email": "required"},
			wantErrors: map[string]interface{}{"email": "required"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bond.ResetForTesting()
			defer bond.ResetForTesting()

			Initialize(Config{
				RootTemplate: defaultTemplate,
				Version:      "1.0",
			})

			req := httptest.NewRequest("POST", "/", nil)
			req.Header.Set("X-Inertia", "true")
			rec := httptest.NewRecorder()

			err := WithErrors(rec, req, tt.component, tt.props, tt.errors)
			if err != nil {
				t.Fatalf("WithErrors failed: %v", err)
			}

			var response map[string]interface{}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatalf("Failed to parse JSON response: %v", err)
			}

			props := response["props"].(map[string]interface{})
			errors, ok := props["errors"]

			if tt.wantErrors == nil {
				if ok && errors != nil {
					t.Errorf("Expected no errors prop, got %v", errors)
				}
				return
			}

			if !ok {
				t.Fatal("Expected errors to be present in props")
			}

			errorsMap := errors.(map[string]interface{})
			if len(errorsMap) != len(tt.wantErrors) {
				t.Errorf("errors has %d items, want %d", len(errorsMap), len(tt.wantErrors))
			}

			for key, want := range tt.wantErrors {
				if errorsMap[key] != want {
					t.Errorf("errors[%s] = %v, want %v", key, errorsMap[key], want)
				}
			}
		})
	}
}

func TestLazyProp_Backward_Compatibility(t *testing.T) {
	tests := []struct {
		name      string
		value     any
		wantValue any
	}{
		{
			name:      "wraps string value",
			value:     "lazy string",
			wantValue: "lazy string",
		},
		{
			name:      "wraps integer value",
			value:     123,
			wantValue: 123,
		},
		{
			name:      "wraps nil value",
			value:     nil,
			wantValue: nil,
		},
		{
			name:      "wraps struct value",
			value:     struct{ Name string }{Name: "test"},
			wantValue: struct{ Name string }{Name: "test"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prop := LazyProp(tt.value)

			// Verify returns correct bond type (OptionalProp)
			if _, ok := interface{}(prop).(bond.OptionalProp); !ok {
				t.Error("LazyProp did not return bond.OptionalProp type")
			}

			// Verify Evaluate returns the wrapped value
			got, err := prop.Evaluate()
			if err != nil {
				t.Errorf("Evaluate() returned error: %v", err)
				return
			}

			// Compare values
			switch v := tt.wantValue.(type) {
			case struct{ Name string }:
				gotStruct, ok := got.(struct{ Name string })
				if !ok {
					t.Errorf("Evaluate() returned wrong type, got %T", got)
					return
				}
				if gotStruct != v {
					t.Errorf("Evaluate() = %v, want %v", gotStruct, v)
				}
			default:
				if got != tt.wantValue {
					t.Errorf("Evaluate() = %v, want %v", got, tt.wantValue)
				}
			}
		})
	}
}
