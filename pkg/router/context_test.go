package router

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
)

func TestNewContext(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	c := NewContext(w, req)

	if c.Request != req {
		t.Error("Expected Request to be set")
	}
	if c.Response != w {
		t.Error("Expected Response to be set")
	}
}

func TestContext_Param(t *testing.T) {
	req := httptest.NewRequest("GET", "/users/123", nil)
	req = mux.SetURLVars(req, map[string]string{"id": "123"})
	w := httptest.NewRecorder()

	c := NewContext(w, req)

	if c.Param("id") != "123" {
		t.Errorf("Expected param 'id' to be '123', got '%s'", c.Param("id"))
	}
	if c.Param("nonexistent") != "" {
		t.Error("Expected nonexistent param to return empty string")
	}
}

func TestContext_Query(t *testing.T) {
	req := httptest.NewRequest("GET", "/test?name=john&age=30", nil)
	w := httptest.NewRecorder()

	c := NewContext(w, req)

	if c.Query("name") != "john" {
		t.Errorf("Expected query 'name' to be 'john', got '%s'", c.Query("name"))
	}
	if c.Query("age") != "30" {
		t.Errorf("Expected query 'age' to be '30', got '%s'", c.Query("age"))
	}
	if c.Query("nonexistent") != "" {
		t.Error("Expected nonexistent query to return empty string")
	}
}

func TestContext_QueryDefault(t *testing.T) {
	req := httptest.NewRequest("GET", "/test?name=john", nil)
	w := httptest.NewRecorder()

	c := NewContext(w, req)

	if c.QueryDefault("name", "default") != "john" {
		t.Error("Expected existing query to return its value")
	}
	if c.QueryDefault("missing", "default") != "default" {
		t.Error("Expected missing query to return default value")
	}
}

func TestContext_Header(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Custom-Header", "custom-value")
	w := httptest.NewRecorder()

	c := NewContext(w, req)

	if c.Header("X-Custom-Header") != "custom-value" {
		t.Error("Expected header to be returned")
	}
	if c.Header("Nonexistent") != "" {
		t.Error("Expected nonexistent header to return empty string")
	}
}

func TestContext_SetHeader(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	c := NewContext(w, req)
	c.SetHeader("X-Custom-Header", "custom-value")

	if w.Header().Get("X-Custom-Header") != "custom-value" {
		t.Error("Expected header to be set on response")
	}
}

func TestContext_Cookie(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: "abc123"})
	w := httptest.NewRecorder()

	c := NewContext(w, req)

	cookie, err := c.Cookie("session")
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if cookie.Value != "abc123" {
		t.Errorf("Expected cookie value 'abc123', got '%s'", cookie.Value)
	}

	_, err = c.Cookie("nonexistent")
	if err == nil {
		t.Error("Expected error for nonexistent cookie")
	}
}

func TestContext_SetCookie(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	c := NewContext(w, req)
	c.SetCookie(&http.Cookie{Name: "session", Value: "xyz789"})

	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("Expected 1 cookie, got %d", len(cookies))
	}
	if cookies[0].Value != "xyz789" {
		t.Errorf("Expected cookie value 'xyz789', got '%s'", cookies[0].Value)
	}
}

func TestContext_JSON(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	c := NewContext(w, req)
	data := map[string]string{"message": "hello"}
	err := c.JSON(http.StatusOK, data)

	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
	if w.Header().Get("Content-Type") != "application/json" {
		t.Error("Expected Content-Type to be application/json")
	}

	var result map[string]string
	json.Unmarshal(w.Body.Bytes(), &result)
	if result["message"] != "hello" {
		t.Error("Expected JSON body to contain message")
	}
}

func TestContext_String(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	c := NewContext(w, req)
	err := c.String(http.StatusOK, "Hello, World!")

	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "text/plain") {
		t.Error("Expected Content-Type to contain text/plain")
	}
	if w.Body.String() != "Hello, World!" {
		t.Errorf("Expected body 'Hello, World!', got '%s'", w.Body.String())
	}
}

func TestContext_HTML(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	c := NewContext(w, req)
	err := c.HTML(http.StatusOK, "<h1>Hello</h1>")

	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "text/html") {
		t.Error("Expected Content-Type to contain text/html")
	}
	if w.Body.String() != "<h1>Hello</h1>" {
		t.Errorf("Expected body '<h1>Hello</h1>', got '%s'", w.Body.String())
	}
}

func TestContext_Redirect(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	c := NewContext(w, req)
	err := c.Redirect(http.StatusFound, "/new-location")

	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if w.Code != http.StatusFound {
		t.Errorf("Expected status 302, got %d", w.Code)
	}
	if w.Header().Get("Location") != "/new-location" {
		t.Error("Expected Location header to be set")
	}
}

func TestContext_Status(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	c := NewContext(w, req)
	err := c.Status(http.StatusAccepted)

	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if w.Code != http.StatusAccepted {
		t.Errorf("Expected status 202, got %d", w.Code)
	}
}

func TestContext_NoContent(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	c := NewContext(w, req)
	err := c.NoContent()

	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if w.Code != http.StatusNoContent {
		t.Errorf("Expected status 204, got %d", w.Code)
	}
}

func TestContext_Bind(t *testing.T) {
	body := `{"name": "John", "age": 30}`
	req := httptest.NewRequest("POST", "/test", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	c := NewContext(w, req)

	var data struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}
	err := c.Bind(&data)

	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if data.Name != "John" {
		t.Errorf("Expected name 'John', got '%s'", data.Name)
	}
	if data.Age != 30 {
		t.Errorf("Expected age 30, got %d", data.Age)
	}
}

func TestContext_Method(t *testing.T) {
	req := httptest.NewRequest("POST", "/test", nil)
	w := httptest.NewRecorder()

	c := NewContext(w, req)

	if c.Method() != "POST" {
		t.Errorf("Expected method 'POST', got '%s'", c.Method())
	}
}

func TestContext_Path(t *testing.T) {
	req := httptest.NewRequest("GET", "/users/123", nil)
	w := httptest.NewRecorder()

	c := NewContext(w, req)

	if c.Path() != "/users/123" {
		t.Errorf("Expected path '/users/123', got '%s'", c.Path())
	}
}

func TestContext_IP(t *testing.T) {
	tests := []struct {
		name     string
		headers  map[string]string
		expected string
	}{
		{
			name:     "X-Forwarded-For header",
			headers:  map[string]string{"X-Forwarded-For": "192.168.1.1"},
			expected: "192.168.1.1",
		},
		{
			name:     "X-Real-IP header",
			headers:  map[string]string{"X-Real-IP": "10.0.0.1"},
			expected: "10.0.0.1",
		},
		{
			name:     "RemoteAddr fallback",
			headers:  map[string]string{},
			expected: "192.0.2.1:1234",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()

			c := NewContext(w, req)
			ip := c.IP()

			if ip != tt.expected {
				t.Errorf("Expected IP '%s', got '%s'", tt.expected, ip)
			}
		})
	}
}

func TestContext_IsAjax(t *testing.T) {
	tests := []struct {
		name     string
		header   string
		expected bool
	}{
		{"AJAX request", "XMLHttpRequest", true},
		{"Normal request", "", false},
		{"Other value", "other", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test", nil)
			if tt.header != "" {
				req.Header.Set("X-Requested-With", tt.header)
			}
			w := httptest.NewRecorder()

			c := NewContext(w, req)

			if c.IsAjax() != tt.expected {
				t.Errorf("Expected IsAjax() to be %v", tt.expected)
			}
		})
	}
}

func TestContext_WantsJSON(t *testing.T) {
	tests := []struct {
		name     string
		accept   string
		inertia  string
		expected bool
	}{
		{"Accept application/json", "application/json", "", true},
		{"Inertia request", "", "true", true},
		{"HTML request", "text/html", "", false},
		{"No headers", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test", nil)
			if tt.accept != "" {
				req.Header.Set("Accept", tt.accept)
			}
			if tt.inertia != "" {
				req.Header.Set("X-Inertia", tt.inertia)
			}
			w := httptest.NewRecorder()

			c := NewContext(w, req)

			if c.WantsJSON() != tt.expected {
				t.Errorf("Expected WantsJSON() to be %v", tt.expected)
			}
		})
	}
}

func TestWrap(t *testing.T) {
	handler := func(c *Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	}

	httpHandler := Wrap(handler)

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	httpHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var result map[string]string
	json.Unmarshal(w.Body.Bytes(), &result)
	if result["status"] != "ok" {
		t.Error("Expected response to contain status: ok")
	}
}

func TestWrap_WithError(t *testing.T) {
	handler := func(c *Context) error {
		return http.ErrBodyNotAllowed
	}

	httpHandler := Wrap(handler)

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	httpHandler(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Expected status 500, got %d", w.Code)
	}
}

func TestContext_Error(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	c := NewContext(w, req)
	err := c.Error(http.StatusBadRequest, "Invalid input")

	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", w.Code)
	}

	var result Error
	json.Unmarshal(w.Body.Bytes(), &result)
	if result.Code != http.StatusBadRequest {
		t.Error("Expected error code in response")
	}
	if result.Message != "Invalid input" {
		t.Error("Expected error message in response")
	}
}

func TestContext_NotFound(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	c := NewContext(w, req)
	c.NotFound()

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", w.Code)
	}

	// Test with custom message
	w2 := httptest.NewRecorder()
	c2 := NewContext(w2, req)
	c2.NotFound("User not found")

	var result Error
	json.Unmarshal(w2.Body.Bytes(), &result)
	if result.Message != "User not found" {
		t.Errorf("Expected custom message, got '%s'", result.Message)
	}
}

func TestContext_BadRequest(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	c := NewContext(w, req)
	c.BadRequest()

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", w.Code)
	}

	// Test with custom message
	w2 := httptest.NewRecorder()
	c2 := NewContext(w2, req)
	c2.BadRequest("Invalid email format")

	var result Error
	json.Unmarshal(w2.Body.Bytes(), &result)
	if result.Message != "Invalid email format" {
		t.Errorf("Expected custom message, got '%s'", result.Message)
	}
}

func TestContext_Unauthorized(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	c := NewContext(w, req)
	c.Unauthorized()

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected status 401, got %d", w.Code)
	}

	// Test with custom message
	w2 := httptest.NewRecorder()
	c2 := NewContext(w2, req)
	c2.Unauthorized("Token expired")

	var result Error
	json.Unmarshal(w2.Body.Bytes(), &result)
	if result.Message != "Token expired" {
		t.Errorf("Expected custom message, got '%s'", result.Message)
	}
}

func TestContext_Forbidden(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	c := NewContext(w, req)
	c.Forbidden()

	if w.Code != http.StatusForbidden {
		t.Errorf("Expected status 403, got %d", w.Code)
	}

	// Test with custom message
	w2 := httptest.NewRecorder()
	c2 := NewContext(w2, req)
	c2.Forbidden("Admin access required")

	var result Error
	json.Unmarshal(w2.Body.Bytes(), &result)
	if result.Message != "Admin access required" {
		t.Errorf("Expected custom message, got '%s'", result.Message)
	}
}
