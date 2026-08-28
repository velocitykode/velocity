package bond

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRender_HTMLResponse_NonInertiaRequest(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/dashboard", nil)

	err := b.Render(w, r, "Dashboard", Props{"user": "Ali"})

	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	contentType := w.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/html") {
		t.Errorf("expected text/html content type, got %s", contentType)
	}

	body := w.Body.String()
	if !strings.Contains(body, `<div id="app"`) {
		t.Error("expected inertia div in response")
	}
	if !strings.Contains(body, `data-page=`) {
		t.Error("expected data-page attribute in response")
	}
}

func TestRender_JSONResponse_InertiaRequest(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	r.Header.Set("X-Inertia", "true")

	err := b.Render(w, r, "Dashboard", Props{"user": "Ali"})

	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("expected application/json content type, got %s", contentType)
	}

	if w.Header().Get("X-Inertia") != "true" {
		t.Error("expected X-Inertia header to be set")
	}

	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("expected X-Content-Type-Options header to be nosniff")
	}

	if w.Header().Get("Vary") != "X-Inertia" {
		t.Error("expected Vary header to be set")
	}

	var page Page
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("failed to parse JSON response: %v", err)
	}

	if page.Component != "Dashboard" {
		t.Errorf("expected component 'Dashboard', got %s", page.Component)
	}
	if page.Props["user"] != "Ali" {
		t.Errorf("expected user 'Ali', got %v", page.Props["user"])
	}
}

func TestRender_IncludesURL(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/users?page=2&sort=name", nil)
	r.Header.Set("X-Inertia", "true")

	err := b.Render(w, r, "Users/Index", Props{})

	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if page.URL != "/users?page=2&sort=name" {
		t.Errorf("expected URL '/users?page=2&sort=name', got %s", page.URL)
	}
}

func TestRender_IncludesVersion(t *testing.T) {
	b, _ := New(Config{
		RootTemplate: validTemplate,
		Version:      "abc123",
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	b.Render(w, r, "Home", Props{})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if page.Version != "abc123" {
		t.Errorf("expected version 'abc123', got %s", page.Version)
	}
}

func TestRender_IncludesEncryptHistory(t *testing.T) {
	b, _ := New(Config{
		RootTemplate:   validTemplate,
		EncryptHistory: true,
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	b.Render(w, r, "Secure", Props{})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if !page.EncryptHistory {
		t.Error("expected encryptHistory to be true")
	}
}

func TestRender_MergesSharedProps(t *testing.T) {
	b := setupBond(t)

	b.Share("appName", "Velocity")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	b.Render(w, r, "Home", Props{"title": "Welcome"})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if page.Props["appName"] != "Velocity" {
		t.Errorf("expected appName 'Velocity', got %v", page.Props["appName"])
	}
	if page.Props["title"] != "Welcome" {
		t.Errorf("expected title 'Welcome', got %v", page.Props["title"])
	}
}

func TestRender_PartialReload_OnlyRequestedProps(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/users", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("X-Inertia-Partial-Data", "users")
	r.Header.Set("X-Inertia-Partial-Component", "Users/Index")

	b.Render(w, r, "Users/Index", Props{
		"users":   []string{"Ali", "Bob"},
		"filters": map[string]string{"status": "active"},
		"stats":   42,
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if _, ok := page.Props["users"]; !ok {
		t.Error("expected users prop to be included")
	}
	if _, ok := page.Props["filters"]; ok {
		t.Error("expected filters prop to be excluded")
	}
	if _, ok := page.Props["stats"]; ok {
		t.Error("expected stats prop to be excluded")
	}
}

func TestRender_PartialReload_MultipleProps(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("X-Inertia-Partial-Data", "users,stats")
	r.Header.Set("X-Inertia-Partial-Component", "Dashboard")

	b.Render(w, r, "Dashboard", Props{
		"users":   []string{"Ali"},
		"stats":   100,
		"filters": "excluded",
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if _, ok := page.Props["users"]; !ok {
		t.Error("expected users to be included")
	}
	if _, ok := page.Props["stats"]; !ok {
		t.Error("expected stats to be included")
	}
	if _, ok := page.Props["filters"]; ok {
		t.Error("expected filters to be excluded")
	}
}

func TestRender_PartialReload_ExceptProps(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("X-Inertia-Partial-Except", "heavy")
	r.Header.Set("X-Inertia-Partial-Component", "Dashboard")

	b.Render(w, r, "Dashboard", Props{
		"light": "included",
		"heavy": "excluded",
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if _, ok := page.Props["light"]; !ok {
		t.Error("expected light to be included")
	}
	if _, ok := page.Props["heavy"]; ok {
		t.Error("expected heavy to be excluded")
	}
}

func TestRender_PartialReload_IgnoresDifferentComponent(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("X-Inertia-Partial-Data", "users")
	r.Header.Set("X-Inertia-Partial-Component", "Other/Component")

	b.Render(w, r, "Users/Index", Props{
		"users":   []string{"Ali"},
		"filters": "should be included",
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	// All props should be included since component doesn't match
	if _, ok := page.Props["users"]; !ok {
		t.Error("expected users to be included")
	}
	if _, ok := page.Props["filters"]; !ok {
		t.Error("expected filters to be included")
	}
}

func TestRender_LazyProp_ExcludedOnInitialLoad(t *testing.T) {
	b := setupBond(t)

	evaluated := false

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	b.Render(w, r, "Home", Props{
		"eager": "always included",
		"lazy": Lazy(func() (any, error) {
			evaluated = true
			return "lazy value", nil
		}),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if evaluated {
		t.Error("lazy prop should not be evaluated on initial load")
	}
	if _, ok := page.Props["lazy"]; ok {
		t.Error("lazy prop should not be in response on initial load")
	}
	if _, ok := page.Props["eager"]; !ok {
		t.Error("eager prop should be in response")
	}
}

func TestRender_LazyProp_EvaluatedOnPartialReload(t *testing.T) {
	b := setupBond(t)

	evaluated := false

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("X-Inertia-Partial-Data", "lazy")
	r.Header.Set("X-Inertia-Partial-Component", "Home")

	b.Render(w, r, "Home", Props{
		"eager": "always included",
		"lazy": Lazy(func() (any, error) {
			evaluated = true
			return "lazy value", nil
		}),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if !evaluated {
		t.Error("lazy prop should be evaluated on partial reload")
	}
	if page.Props["lazy"] != "lazy value" {
		t.Errorf("expected lazy value, got %v", page.Props["lazy"])
	}
}

func TestRender_LazyProp_Error(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("X-Inertia-Partial-Data", "lazy")
	r.Header.Set("X-Inertia-Partial-Component", "Home")

	err := b.Render(w, r, "Home", Props{
		"lazy": Lazy(func() (any, error) {
			return nil, errors.New("lazy evaluation failed")
		}),
	})

	if err == nil {
		t.Error("expected error from lazy prop evaluation")
	}
}

func TestRender_DeferredProp_NotIncludedOnInitialLoad(t *testing.T) {
	b := setupBond(t)

	evaluated := false

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	b.Render(w, r, "Dashboard", Props{
		"quick": "included",
		"slow": Defer(func() (any, error) {
			evaluated = true
			return "slow data", nil
		}),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if evaluated {
		t.Error("deferred prop should not be evaluated on initial load")
	}
	if _, ok := page.Props["slow"]; ok {
		t.Error("deferred prop should not be in response on initial load")
	}
}

func TestRender_DeferredProp_TrackedInDeferredGroups(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	b.Render(w, r, "Dashboard", Props{
		"stats": Defer(func() (any, error) { return nil, nil }),
		"chart": Defer(func() (any, error) { return nil, nil }, "slow"),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if page.DeferredProps == nil {
		t.Fatal("expected deferredProps to be set")
	}
	if !contains(page.DeferredProps["default"], "stats") {
		t.Error("expected stats in default group")
	}
	if !contains(page.DeferredProps["slow"], "chart") {
		t.Error("expected chart in slow group")
	}
}

func TestRender_DeferredProp_EvaluatedOnPartialReload(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("X-Inertia-Partial-Data", "slow")
	r.Header.Set("X-Inertia-Partial-Component", "Dashboard")

	b.Render(w, r, "Dashboard", Props{
		"slow": Defer(func() (any, error) {
			return "slow data", nil
		}),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if page.Props["slow"] != "slow data" {
		t.Errorf("expected slow data, got %v", page.Props["slow"])
	}
}

func TestRender_DeferredProp_NotReannouncedOnPartialReload(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("X-Inertia-Partial-Data", "slow")
	r.Header.Set("X-Inertia-Partial-Component", "Dashboard")

	b.Render(w, r, "Dashboard", Props{
		"slow": Defer(func() (any, error) {
			return "slow data", nil
		}),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	// The partial reload delivers the deferred data; re-announcing the
	// deferred groups there would make the client schedule another partial
	// reload, endlessly.
	if page.DeferredProps != nil {
		t.Errorf("deferredProps must be omitted from partial responses, got %v", page.DeferredProps)
	}
}

func TestRender_DeferredProp_Error(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("X-Inertia-Partial-Data", "slow")
	r.Header.Set("X-Inertia-Partial-Component", "Dashboard")

	err := b.Render(w, r, "Dashboard", Props{
		"slow": Defer(func() (any, error) {
			return nil, errors.New("deferred evaluation failed")
		}),
	})

	if err == nil {
		t.Error("expected error from deferred prop evaluation")
	}
}

func TestRender_AlwaysProp_IncludedOnPartialReload(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("X-Inertia-Partial-Data", "users")
	r.Header.Set("X-Inertia-Partial-Component", "Users/Index")

	b.Render(w, r, "Users/Index", Props{
		"users": []string{"Ali"},
		"auth":  Always(map[string]string{"user": "Ali"}),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if _, ok := page.Props["auth"]; !ok {
		t.Error("always prop should be included on partial reload")
	}
}

func TestRender_AlwaysProp_ExcludedIfExcepted(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("X-Inertia-Partial-Except", "auth")
	r.Header.Set("X-Inertia-Partial-Component", "Users/Index")

	b.Render(w, r, "Users/Index", Props{
		"auth": Always("excluded"),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if _, ok := page.Props["auth"]; ok {
		t.Error("always prop should be excluded if in except list")
	}
}

func TestRender_OptionalProp_BehavesLikeLazy(t *testing.T) {
	b := setupBond(t)

	evaluated := false

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	b.Render(w, r, "Home", Props{
		"optional": Optional(func() (any, error) {
			evaluated = true
			return "optional value", nil
		}),
	})

	if evaluated {
		t.Error("optional prop should not be evaluated on initial load")
	}
}

func TestRender_OptionalProp_EvaluatedOnPartialReload(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("X-Inertia-Partial-Data", "optional")
	r.Header.Set("X-Inertia-Partial-Component", "Home")

	b.Render(w, r, "Home", Props{
		"optional": Optional(func() (any, error) {
			return "optional value", nil
		}),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if page.Props["optional"] != "optional value" {
		t.Errorf("expected optional value, got %v", page.Props["optional"])
	}
}

func TestRender_OptionalProp_Error(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("X-Inertia-Partial-Data", "optional")
	r.Header.Set("X-Inertia-Partial-Component", "Home")

	err := b.Render(w, r, "Home", Props{
		"optional": Optional(func() (any, error) {
			return nil, errors.New("optional evaluation failed")
		}),
	})

	if err == nil {
		t.Error("expected error from optional prop evaluation")
	}
}

func TestRender_HTMLContainsProperDiv(t *testing.T) {
	b, _ := New(Config{
		RootTemplate: validTemplate,
		ContainerID:  "custom-app",
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	b.Render(w, r, "Home", Props{})

	body := w.Body.String()
	if !strings.Contains(body, `<div id="custom-app"`) {
		t.Error("expected custom container ID in HTML")
	}
}

func TestBuildInertiaDiv(t *testing.T) {
	b := setupBond(t)

	div := b.buildInertiaDiv(`{"test":true}`)

	expected := `<div id="app" data-page='{"test":true}'></div>`
	if div != expected {
		t.Errorf("expected %s, got %s", expected, div)
	}
}

func TestIsPartialReload_True(t *testing.T) {
	b := setupBond(t)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia-Partial-Data", "users")
	r.Header.Set("X-Inertia-Partial-Component", "Users/Index")

	if !b.isPartialReload(r, "Users/Index") {
		t.Error("expected isPartialReload to return true")
	}
}

func TestIsPartialReload_FalseWhenNoData(t *testing.T) {
	b := setupBond(t)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia-Partial-Component", "Users/Index")

	if b.isPartialReload(r, "Users/Index") {
		t.Error("expected isPartialReload to return false when no data")
	}
}

func TestIsPartialReload_TrueWithExceptOnly(t *testing.T) {
	b := setupBond(t)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia-Partial-Except", "heavy")
	r.Header.Set("X-Inertia-Partial-Component", "Users/Index")

	if !b.isPartialReload(r, "Users/Index") {
		t.Error("expected isPartialReload to return true with except header")
	}
}

func TestIsPartialReload_FalseWhenComponentMismatch(t *testing.T) {
	b := setupBond(t)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia-Partial-Data", "users")
	r.Header.Set("X-Inertia-Partial-Component", "Other/Component")

	if b.isPartialReload(r, "Users/Index") {
		t.Error("expected isPartialReload to return false for different component")
	}
}

func TestGetPartialOnly(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia-Partial-Data", "users,stats,filters")

	only := getPartialOnly(r)

	if len(only) != 3 {
		t.Errorf("expected 3 items, got %d", len(only))
	}
	if !contains(only, "users") || !contains(only, "stats") || !contains(only, "filters") {
		t.Error("expected all items to be present")
	}
}

func TestGetPartialOnly_Empty(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	only := getPartialOnly(r)

	if only != nil {
		t.Errorf("expected nil, got %v", only)
	}
}

func TestGetPartialExcept(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia-Partial-Except", "heavy,slow")

	except := getPartialExcept(r)

	if len(except) != 2 {
		t.Errorf("expected 2 items, got %d", len(except))
	}
}

func TestGetPartialExcept_Empty(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	except := getPartialExcept(r)

	if except != nil {
		t.Errorf("expected nil, got %v", except)
	}
}

func TestExtractDeferredGroups_Empty(t *testing.T) {
	b := setupBond(t)

	groups := b.extractDeferredGroups(Props{
		"regular": "value",
	})

	if groups != nil {
		t.Errorf("expected nil for no deferred props, got %v", groups)
	}
}

func TestShouldIncludeInPartial(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		only     []string
		except   []string
		expected bool
	}{
		{"no filters", "key", nil, nil, true},
		{"in only list", "key", []string{"key", "other"}, nil, true},
		{"not in only list", "key", []string{"other"}, nil, false},
		{"in except list", "key", nil, []string{"key"}, false},
		{"not in except list", "key", nil, []string{"other"}, true},
		{"in both lists", "key", []string{"key"}, []string{"key"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shouldIncludeInPartial(tt.key, tt.only, tt.except)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestContains(t *testing.T) {
	slice := []string{"a", "b", "c"}

	if !contains(slice, "a") {
		t.Error("expected to find 'a'")
	}
	if !contains(slice, "b") {
		t.Error("expected to find 'b'")
	}
	if contains(slice, "d") {
		t.Error("expected to not find 'd'")
	}
	if contains(nil, "a") {
		t.Error("expected to not find in nil slice")
	}
	if contains([]string{}, "a") {
		t.Error("expected to not find in empty slice")
	}
}

func TestRender_HTML_WithUnmarshalableProps_ReturnsError(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	// Non-Inertia request triggers HTML rendering

	err := b.Render(w, r, "Home", Props{
		"badValue": make(chan int), // Cannot be JSON marshaled
	})

	if err == nil {
		t.Error("expected error for unmarshalable props in HTML render")
	}
}

// --- MergeProp render tests ---

func TestRender_MergeProp_IncludedOnInitialLoad(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	b.Render(w, r, "Home", Props{
		"items": Merge([]string{"a", "b"}),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if page.Props["items"] == nil {
		t.Error("merge prop should be included on initial load")
	}
	if !contains(page.MergeProps, "items") {
		t.Error("expected items in mergeProps")
	}
}

func TestRender_MergeProp_PrependTracked(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	b.Render(w, r, "Home", Props{
		"items": Merge([]string{"a"}).Prepend("rows"),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if !contains(page.PrependProps, "items") {
		t.Error("expected items in prependProps")
	}
}

func TestRender_MergeProp_DeepMergeTracked(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	b.Render(w, r, "Home", Props{
		"settings": Merge(map[string]any{"theme": "dark"}).DeepMerge(),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if !contains(page.MergeProps, "settings") {
		t.Error("expected settings in mergeProps")
	}
	if !contains(page.DeepMergeProps, "settings") {
		t.Error("expected settings in deepMergeProps")
	}
}

func TestRender_MergeProp_MatchOnTracked(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	b.Render(w, r, "Home", Props{
		"items": Merge([]string{"a"}).MatchOn("id"),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if page.MatchPropsOn == nil || len(page.MatchPropsOn["items"]) != 1 {
		t.Error("expected matchPropsOn to contain items with 1 key")
	}
}

func TestRender_MergeProp_OnPartialReload(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("X-Inertia-Partial-Data", "items")
	r.Header.Set("X-Inertia-Partial-Component", "Home")

	b.Render(w, r, "Home", Props{
		"items": Merge([]string{"c", "d"}),
		"other": "excluded",
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if page.Props["items"] == nil {
		t.Error("merge prop should be included in partial reload")
	}
	if _, ok := page.Props["other"]; ok {
		t.Error("non-requested prop should be excluded")
	}
	if !contains(page.MergeProps, "items") {
		t.Error("expected items in mergeProps")
	}
}

func TestRender_MergeProp_Once_SkippedIfAlreadySeen(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set(HeaderExceptOnceProps, "items")

	b.Render(w, r, "Home", Props{
		"items": Merge([]string{"a"}).Once(),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if _, ok := page.Props["items"]; ok {
		t.Error("once merge prop should be skipped when already seen")
	}
}

func TestRender_MergeProp_Error(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	err := b.Render(w, r, "Home", Props{
		"items": MergeFunc(func() (any, error) {
			return nil, errors.New("merge eval failed")
		}),
	})

	if err == nil {
		t.Error("expected error from merge prop evaluation")
	}
}

// --- OnceProp render tests ---

func TestRender_OnceProp_IncludedOnInitialLoad(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	b.Render(w, r, "Home", Props{
		"token": Once(func() (any, error) { return "abc123", nil }),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if page.Props["token"] != "abc123" {
		t.Errorf("expected token 'abc123', got %v", page.Props["token"])
	}
	if page.OnceProps == nil || page.OnceProps["token"].Prop != "token" {
		t.Error("expected token in onceProps")
	}
}

func TestRender_OnceProp_SkippedIfAlreadySeen(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set(HeaderExceptOnceProps, "token")

	b.Render(w, r, "Home", Props{
		"token": Once(func() (any, error) { return "abc123", nil }),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if _, ok := page.Props["token"]; ok {
		t.Error("once prop should be skipped when client already has it")
	}
}

func TestRender_OnceProp_CustomKey(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set(HeaderExceptOnceProps, "my-token")

	b.Render(w, r, "Home", Props{
		"token": Once(func() (any, error) { return "abc", nil }).As("my-token"),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if _, ok := page.Props["token"]; ok {
		t.Error("once prop with custom key should be skipped when key is in except list")
	}
}

func TestRender_OnceProp_NotSkippedWithDifferentKey(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set(HeaderExceptOnceProps, "other-key")

	b.Render(w, r, "Home", Props{
		"token": Once(func() (any, error) { return "abc", nil }).As("my-token"),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if page.Props["token"] != "abc" {
		t.Error("once prop should be included when except key doesn't match")
	}
}

func TestRender_OnceProp_OnPartialReload(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("X-Inertia-Partial-Data", "token")
	r.Header.Set("X-Inertia-Partial-Component", "Home")

	b.Render(w, r, "Home", Props{
		"token": Once(func() (any, error) { return "abc", nil }),
		"other": "excluded",
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if page.Props["token"] != "abc" {
		t.Error("once prop should be included in partial reload when requested")
	}
	if _, ok := page.Props["other"]; ok {
		t.Error("non-requested prop should be excluded")
	}
}

func TestRender_OnceProp_Error(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	err := b.Render(w, r, "Home", Props{
		"token": Once(func() (any, error) { return nil, errors.New("once failed") }),
	})

	if err == nil {
		t.Error("expected error from once prop evaluation")
	}
}

// --- ScrollProp render tests ---

func TestRender_ScrollProp_IncludedOnInitialLoad(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	b.Render(w, r, "Feed", Props{
		"posts": Scroll([]string{"post1", "post2"}, "data"),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if page.Props["posts"] == nil {
		t.Error("scroll prop should be included on initial load")
	}
	if !contains(page.MergeProps, "posts") {
		t.Error("expected posts in mergeProps (scroll defaults to append)")
	}
}

func TestRender_ScrollProp_WithMetadata(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	b.Render(w, r, "Feed", Props{
		"posts": Scroll([]string{"post1"}, "data").WithMetadata(func() ScrollMeta {
			return ScrollMeta{
				PageName:    "page",
				CurrentPage: 1,
				NextPage:    2,
			}
		}),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if page.ScrollProps == nil {
		t.Fatal("expected scrollProps to be set")
	}
	meta, ok := page.ScrollProps["posts"]
	if !ok {
		t.Fatal("expected posts in scrollProps")
	}
	if meta.PageName != "page" {
		t.Errorf("expected PageName 'page', got %s", meta.PageName)
	}
	if meta.CurrentPage != float64(1) {
		t.Errorf("expected CurrentPage 1, got %v", meta.CurrentPage)
	}
}

func TestRender_ScrollProp_Deferred_NotOnInitialLoad(t *testing.T) {
	b := setupBond(t)

	evaluated := false
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	b.Render(w, r, "Feed", Props{
		"posts": ScrollFunc(func() (any, error) {
			evaluated = true
			return "data", nil
		}, "data").Defer(),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if evaluated {
		t.Error("deferred scroll prop should not be evaluated on initial load")
	}
	if _, ok := page.Props["posts"]; ok {
		t.Error("deferred scroll prop should not be in response on initial load")
	}
}

func TestRender_ScrollProp_Deferred_EvaluatedOnPartialReload(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("X-Inertia-Partial-Data", "posts")
	r.Header.Set("X-Inertia-Partial-Component", "Feed")

	b.Render(w, r, "Feed", Props{
		"posts": Scroll([]string{"post1"}, "data").Defer(),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if page.Props["posts"] == nil {
		t.Error("deferred scroll prop should be included on partial reload")
	}
	if !contains(page.MergeProps, "posts") {
		t.Error("expected posts in mergeProps")
	}
}

func TestRender_ScrollProp_PrependViaHeader(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set(HeaderInfiniteScrollIntent, "prepend")

	b.Render(w, r, "Feed", Props{
		"posts": Scroll([]string{"post1"}, "data"),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if !contains(page.PrependProps, "posts") {
		t.Error("expected posts in prependProps when header says prepend")
	}
}

func TestRender_ScrollProp_AppendViaHeader(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set(HeaderInfiniteScrollIntent, "append")

	// Prop says prepend but header overrides to append
	b.Render(w, r, "Feed", Props{
		"posts": Scroll([]string{"post1"}, "data").PrependData(),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if !contains(page.MergeProps, "posts") {
		t.Error("expected posts in mergeProps (header overrides to append)")
	}
	if contains(page.PrependProps, "posts") {
		t.Error("posts should not be in prependProps when header says append")
	}
}

func TestRender_ScrollProp_DeferredTrackedInDeferredGroups(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	b.Render(w, r, "Feed", Props{
		"posts": Scroll([]string{"a"}, "data").Defer("scroll"),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if page.DeferredProps == nil {
		t.Fatal("expected deferredProps to be set")
	}
	if !contains(page.DeferredProps["scroll"], "posts") {
		t.Error("expected posts in scroll deferred group")
	}
}

func TestRender_ScrollProp_Error(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	err := b.Render(w, r, "Feed", Props{
		"posts": ScrollFunc(func() (any, error) {
			return nil, errors.New("scroll failed")
		}, "data"),
	})

	if err == nil {
		t.Error("expected error from scroll prop evaluation")
	}
}

// --- DeferredProp merge render tests ---

func TestRender_DeferredProp_WithMerge(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("X-Inertia-Partial-Data", "items")
	r.Header.Set("X-Inertia-Partial-Component", "List")

	b.Render(w, r, "List", Props{
		"items": Defer(func() (any, error) { return []int{1, 2}, nil }).Merge(),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if page.Props["items"] == nil {
		t.Error("deferred merge prop should be included in partial reload")
	}
	if !contains(page.MergeProps, "items") {
		t.Error("expected items in mergeProps")
	}
}

func TestRender_DeferredProp_WithOnce_SkippedIfSeen(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("X-Inertia-Partial-Data", "items")
	r.Header.Set("X-Inertia-Partial-Component", "List")
	r.Header.Set(HeaderExceptOnceProps, "items")

	b.Render(w, r, "List", Props{
		"items": Defer(func() (any, error) { return []int{1}, nil }).Once(),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if _, ok := page.Props["items"]; ok {
		t.Error("deferred once prop should be skipped when already seen")
	}
}

// --- OptionalProp once render tests ---

func TestRender_OptionalProp_Once_SkippedIfSeen(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("X-Inertia-Partial-Data", "extra")
	r.Header.Set("X-Inertia-Partial-Component", "Home")
	r.Header.Set(HeaderExceptOnceProps, "extra")

	b.Render(w, r, "Home", Props{
		"extra": Optional(func() (any, error) { return "val", nil }).Once(),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if _, ok := page.Props["extra"]; ok {
		t.Error("optional once prop should be skipped when already seen")
	}
}

func TestRender_OptionalProp_Once_CustomKey(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("X-Inertia-Partial-Data", "extra")
	r.Header.Set("X-Inertia-Partial-Component", "Home")
	r.Header.Set(HeaderExceptOnceProps, "my-extra")

	b.Render(w, r, "Home", Props{
		"extra": Optional(func() (any, error) { return "val", nil }).Once().As("my-extra"),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if _, ok := page.Props["extra"]; ok {
		t.Error("optional once prop with custom key should be skipped")
	}
}

func TestRender_OptionalProp_Once_Included(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("X-Inertia-Partial-Data", "extra")
	r.Header.Set("X-Inertia-Partial-Component", "Home")

	b.Render(w, r, "Home", Props{
		"extra": Optional(func() (any, error) { return "val", nil }).Once(),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if page.Props["extra"] != "val" {
		t.Error("optional once prop should be included when not in except list")
	}
	if page.OnceProps == nil || page.OnceProps["extra"].Prop != "extra" {
		t.Error("expected extra in onceProps")
	}
}

// --- Reset header tests ---

func TestRender_ResetHeader_ClearsMergeMetadata(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("X-Inertia-Partial-Data", "items")
	r.Header.Set("X-Inertia-Partial-Component", "List")
	r.Header.Set(HeaderReset, "items")

	b.Render(w, r, "List", Props{
		"items": Merge([]string{"a", "b"}),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if contains(page.MergeProps, "items") {
		t.Error("items should be cleared from mergeProps on reset")
	}
}

func TestRender_ResetHeader_OnlyAffectsSpecifiedKeys(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("X-Inertia-Partial-Data", "items,other")
	r.Header.Set("X-Inertia-Partial-Component", "List")
	r.Header.Set(HeaderReset, "items")

	b.Render(w, r, "List", Props{
		"items": Merge([]string{"a"}),
		"other": Merge([]string{"b"}),
	})

	var page Page
	json.Unmarshal(w.Body.Bytes(), &page)

	if contains(page.MergeProps, "items") {
		t.Error("items should be cleared from mergeProps on reset")
	}
	if !contains(page.MergeProps, "other") {
		t.Error("other should remain in mergeProps")
	}
}

// --- Header constant tests ---

func TestHeaderConstants(t *testing.T) {
	tests := []struct {
		name     string
		constant string
		expected string
	}{
		{"Inertia", HeaderInertia, "X-Inertia"},
		{"Version", HeaderVersion, "X-Inertia-Version"},
		{"Location", HeaderLocation, "X-Inertia-Location"},
		{"PartialComponent", HeaderPartialComponent, "X-Inertia-Partial-Component"},
		{"PartialOnly", HeaderPartialOnly, "X-Inertia-Partial-Data"},
		{"PartialExcept", HeaderPartialExcept, "X-Inertia-Partial-Except"},
		{"Reset", HeaderReset, "X-Inertia-Reset"},
		{"ExceptOnceProps", HeaderExceptOnceProps, "X-Inertia-Except-Once-Props"},
		{"InfiniteScrollIntent", HeaderInfiniteScrollIntent, "X-Inertia-Infinite-Scroll-Merge-Intent"},
		{"ErrorBag", HeaderErrorBag, "X-Inertia-Error-Bag"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.constant != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, tt.constant)
			}
		})
	}
}

// --- Helper function tests ---

func TestSplitHeader(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int
	}{
		{"empty", "", 0},
		{"single", "items", 1},
		{"multiple", "a,b,c", 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := splitHeader(tt.input)
			if tt.expected == 0 {
				if result != nil {
					t.Errorf("expected nil, got %v", result)
				}
			} else if len(result) != tt.expected {
				t.Errorf("expected %d items, got %d", tt.expected, len(result))
			}
		})
	}
}

func TestAppendUnique(t *testing.T) {
	slice := []string{"a", "b"}

	result := appendUnique(slice, "c")
	if len(result) != 3 {
		t.Errorf("expected 3 items, got %d", len(result))
	}

	result = appendUnique(result, "b")
	if len(result) != 3 {
		t.Errorf("expected 3 items (no dup), got %d", len(result))
	}
}

func TestRemoveKeys(t *testing.T) {
	slice := []string{"a", "b", "c"}

	result := removeKeys(slice, []string{"b"})
	if len(result) != 2 {
		t.Errorf("expected 2 items, got %d", len(result))
	}
	if contains(result, "b") {
		t.Error("expected b to be removed")
	}

	// Remove all
	result = removeKeys(slice, []string{"a", "b", "c"})
	if result != nil {
		t.Errorf("expected nil when all removed, got %v", result)
	}

	// Empty keys
	result = removeKeys(slice, nil)
	if len(result) != 3 {
		t.Errorf("expected 3 items when no keys to remove, got %d", len(result))
	}
}
