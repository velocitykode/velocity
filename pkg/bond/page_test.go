package bond

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPage_ToJSON(t *testing.T) {
	page := Page{
		Component: "Dashboard",
		Props: Props{
			"user": "Ali",
			"count": 42,
		},
		URL:     "/dashboard",
		Version: "1.0.0",
	}

	jsonStr, err := page.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON failed: %v", err)
	}

	// Verify it's valid JSON
	var parsed map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if parsed["component"] != "Dashboard" {
		t.Errorf("expected component 'Dashboard', got %v", parsed["component"])
	}
	if parsed["url"] != "/dashboard" {
		t.Errorf("expected url '/dashboard', got %v", parsed["url"])
	}
	if parsed["version"] != "1.0.0" {
		t.Errorf("expected version '1.0.0', got %v", parsed["version"])
	}
}

func TestPage_ToJSON_WithProps(t *testing.T) {
	page := Page{
		Component: "Users/Index",
		Props: Props{
			"users": []string{"Ali", "Bob"},
			"meta": map[string]any{
				"total": 2,
				"page":  1,
			},
		},
		URL:     "/users",
		Version: "1",
	}

	jsonStr, err := page.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON failed: %v", err)
	}

	if !strings.Contains(jsonStr, `"users"`) {
		t.Error("expected JSON to contain 'users' key")
	}
	if !strings.Contains(jsonStr, `"meta"`) {
		t.Error("expected JSON to contain 'meta' key")
	}
}

func TestPage_ToJSON_EmptyProps(t *testing.T) {
	page := Page{
		Component: "Empty",
		Props:     Props{},
		URL:       "/",
		Version:   "1",
	}

	jsonStr, err := page.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON failed: %v", err)
	}

	if !strings.Contains(jsonStr, `"props":{}`) {
		t.Errorf("expected empty props object, got %s", jsonStr)
	}
}

func TestPage_ToJSON_NilProps(t *testing.T) {
	page := Page{
		Component: "Empty",
		Props:     nil,
		URL:       "/",
		Version:   "1",
	}

	jsonStr, err := page.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON failed: %v", err)
	}

	if !strings.Contains(jsonStr, `"props":null`) {
		t.Errorf("expected null props, got %s", jsonStr)
	}
}

func TestPage_ToJSON_EncryptHistory(t *testing.T) {
	page := Page{
		Component:      "Secure",
		Props:          Props{},
		URL:            "/secure",
		Version:        "1",
		EncryptHistory: true,
	}

	jsonStr, err := page.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON failed: %v", err)
	}

	if !strings.Contains(jsonStr, `"encryptHistory":true`) {
		t.Errorf("expected encryptHistory:true, got %s", jsonStr)
	}
}

func TestPage_ToJSON_EncryptHistory_Omitted(t *testing.T) {
	page := Page{
		Component:      "Normal",
		Props:          Props{},
		URL:            "/normal",
		Version:        "1",
		EncryptHistory: false,
	}

	jsonStr, err := page.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON failed: %v", err)
	}

	// omitempty should exclude false value
	if strings.Contains(jsonStr, `"encryptHistory"`) {
		t.Errorf("expected encryptHistory to be omitted, got %s", jsonStr)
	}
}

func TestPage_ToJSON_ClearHistory(t *testing.T) {
	page := Page{
		Component:    "Reset",
		Props:        Props{},
		URL:          "/reset",
		Version:      "1",
		ClearHistory: true,
	}

	jsonStr, err := page.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON failed: %v", err)
	}

	if !strings.Contains(jsonStr, `"clearHistory":true`) {
		t.Errorf("expected clearHistory:true, got %s", jsonStr)
	}
}

func TestPage_ToJSON_DeferredProps(t *testing.T) {
	page := Page{
		Component: "Dashboard",
		Props:     Props{},
		URL:       "/dashboard",
		Version:   "1",
		DeferredProps: map[string][]string{
			"default": {"stats", "notifications"},
			"slow":    {"analytics"},
		},
	}

	jsonStr, err := page.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON failed: %v", err)
	}

	if !strings.Contains(jsonStr, `"deferredProps"`) {
		t.Errorf("expected deferredProps, got %s", jsonStr)
	}
	if !strings.Contains(jsonStr, `"default"`) {
		t.Errorf("expected default group, got %s", jsonStr)
	}
	if !strings.Contains(jsonStr, `"slow"`) {
		t.Errorf("expected slow group, got %s", jsonStr)
	}
}

func TestPage_ToHTMLAttr(t *testing.T) {
	page := Page{
		Component: "Test",
		Props: Props{
			"message": "Hello <World> & \"Friends\"",
		},
		URL:     "/test",
		Version: "1",
	}

	attr, err := page.ToHTMLAttr()
	if err != nil {
		t.Fatalf("ToHTMLAttr failed: %v", err)
	}

	// json.Marshal escapes <, >, & as unicode: \u003c, \u003e, \u0026
	// html.EscapeString then escapes quotes: " becomes &#34;
	// The result should be safe for embedding in data-page='...'

	// Raw < should not be present (escaped by json.Marshal as \u003c)
	if strings.Contains(attr, "<World>") {
		t.Error("expected < and > to be escaped by JSON encoder")
	}

	// Double quotes should be HTML escaped
	if strings.Contains(attr, `"component"`) {
		t.Error("expected double quotes to be HTML escaped")
	}

	// Verify HTML-safe escaping happened
	if !strings.Contains(attr, "&#34;") {
		t.Error("expected &#34; for escaped double quotes")
	}
}

func TestPage_ToHTMLAttr_SafeForDataAttribute(t *testing.T) {
	page := Page{
		Component: "Test",
		Props: Props{
			"single": "It's a test",
		},
		URL:     "/test",
		Version: "1",
	}

	attr, err := page.ToHTMLAttr()
	if err != nil {
		t.Fatalf("ToHTMLAttr failed: %v", err)
	}

	// Single quotes should be safe since we use data-page='...'
	// The apostrophe in "It's" is inside a JSON string value (double-quoted)
	if !strings.Contains(attr, "It") {
		t.Errorf("expected 'It' in output, got %s", attr)
	}
}

func TestPage_ToJSON_URLWithQueryString(t *testing.T) {
	page := Page{
		Component: "Search",
		Props:     Props{},
		URL:       "/search?q=hello&page=2",
		Version:   "1",
	}

	jsonStr, err := page.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON failed: %v", err)
	}

	// URL should be preserved exactly
	var parsed Page
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if parsed.URL != "/search?q=hello&page=2" {
		t.Errorf("expected URL '/search?q=hello&page=2', got %s", parsed.URL)
	}
}

func TestPage_ToJSON_NestedComponentPath(t *testing.T) {
	page := Page{
		Component: "Users/Profile/Settings",
		Props:     Props{},
		URL:       "/users/1/settings",
		Version:   "1",
	}

	jsonStr, err := page.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON failed: %v", err)
	}

	var parsed Page
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if parsed.Component != "Users/Profile/Settings" {
		t.Errorf("expected component 'Users/Profile/Settings', got %s", parsed.Component)
	}
}

func TestPage_ToJSON_UnmarshalableValue_ReturnsError(t *testing.T) {
	// Create a prop with a channel, which cannot be marshaled to JSON
	page := Page{
		Component: "Test",
		Props: Props{
			"badValue": make(chan int),
		},
		URL:     "/test",
		Version: "1",
	}

	_, err := page.ToJSON()
	if err == nil {
		t.Error("expected error for unmarshalable value")
	}
}

func TestPage_ToHTMLAttr_UnmarshalableValue_ReturnsError(t *testing.T) {
	// Create a prop with a channel, which cannot be marshaled to JSON
	page := Page{
		Component: "Test",
		Props: Props{
			"badValue": make(chan int),
		},
		URL:     "/test",
		Version: "1",
	}

	_, err := page.ToHTMLAttr()
	if err == nil {
		t.Error("expected error for unmarshalable value")
	}
}
