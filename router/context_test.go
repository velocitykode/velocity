package router

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/resource"
	"github.com/velocitykode/velocity/validation"
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
	req = SetParams(req, map[string]string{"id": "123"})
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
			name:     "X-Forwarded-For header ignored (untrusted)",
			headers:  map[string]string{"X-Forwarded-For": "192.168.1.1"},
			expected: "192.0.2.1",
		},
		{
			name:     "X-Real-IP header ignored (untrusted)",
			headers:  map[string]string{"X-Real-IP": "10.0.0.1"},
			expected: "192.0.2.1",
		},
		{
			name:     "RemoteAddr with port stripped",
			headers:  map[string]string{},
			expected: "192.0.2.1",
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

// ---------------------------------------------------------------------------
// BindForm tests
// ---------------------------------------------------------------------------

func TestContext_BindForm(t *testing.T) {
	type formData struct {
		Name    string  `form:"name"`
		Age     int     `form:"age"`
		Score   float64 `form:"score"`
		Active  bool    `form:"active"`
		Ignored string  // no tag
	}

	body := strings.NewReader("name=Alice&age=30&score=9.5&active=true")
	req := httptest.NewRequest("POST", "/test", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	c := NewContext(w, req)

	var fd formData
	if err := c.BindForm(&fd); err != nil {
		t.Fatalf("BindForm error: %v", err)
	}
	if fd.Name != "Alice" {
		t.Errorf("expected Name=Alice, got %s", fd.Name)
	}
	if fd.Age != 30 {
		t.Errorf("expected Age=30, got %d", fd.Age)
	}
	if fd.Score != 9.5 {
		t.Errorf("expected Score=9.5, got %f", fd.Score)
	}
	if !fd.Active {
		t.Error("expected Active=true")
	}
	if fd.Ignored != "" {
		t.Error("expected Ignored to be empty")
	}
}

func TestContext_BindForm_Int64(t *testing.T) {
	type formData struct {
		ID int64 `form:"id"`
	}

	body := strings.NewReader("id=9999999999")
	req := httptest.NewRequest("POST", "/test", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	c := NewContext(w, req)
	var fd formData
	if err := c.BindForm(&fd); err != nil {
		t.Fatalf("BindForm error: %v", err)
	}
	if fd.ID != 9999999999 {
		t.Errorf("expected ID=9999999999, got %d", fd.ID)
	}
}

func TestContext_BindForm_InvalidInt(t *testing.T) {
	type formData struct {
		Age int `form:"age"`
	}
	body := strings.NewReader("age=notanumber")
	req := httptest.NewRequest("POST", "/test", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	c := NewContext(w, req)

	var fd formData
	if err := c.BindForm(&fd); err == nil {
		t.Error("expected error for invalid int")
	}
}

func TestContext_BindForm_NonPointer(t *testing.T) {
	type formData struct {
		Name string `form:"name"`
	}
	body := strings.NewReader("name=test")
	req := httptest.NewRequest("POST", "/test", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	c := NewContext(w, req)

	var fd formData
	if err := c.BindForm(fd); err == nil {
		t.Error("expected error for non-pointer")
	}
}

// ---------------------------------------------------------------------------
// BindQuery tests
// ---------------------------------------------------------------------------

func TestContext_BindQuery(t *testing.T) {
	type queryData struct {
		Page   int    `query:"page"`
		Search string `query:"q"`
		Limit  int64  `query:"limit"`
	}

	req := httptest.NewRequest("GET", "/test?page=2&q=hello&limit=100", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)

	var qd queryData
	if err := c.BindQuery(&qd); err != nil {
		t.Fatalf("BindQuery error: %v", err)
	}
	if qd.Page != 2 {
		t.Errorf("expected Page=2, got %d", qd.Page)
	}
	if qd.Search != "hello" {
		t.Errorf("expected Search=hello, got %s", qd.Search)
	}
	if qd.Limit != 100 {
		t.Errorf("expected Limit=100, got %d", qd.Limit)
	}
}

// ---------------------------------------------------------------------------
// BindXML tests
// ---------------------------------------------------------------------------

func TestContext_BindXML(t *testing.T) {
	type item struct {
		XMLName xml.Name `xml:"item"`
		Name    string   `xml:"name"`
		Value   int      `xml:"value"`
	}

	body := `<item><name>test</name><value>42</value></item>`
	req := httptest.NewRequest("POST", "/test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	c := NewContext(w, req)

	var it item
	if err := c.BindXML(&it); err != nil {
		t.Fatalf("BindXML error: %v", err)
	}
	if it.Name != "test" {
		t.Errorf("expected Name=test, got %s", it.Name)
	}
	if it.Value != 42 {
		t.Errorf("expected Value=42, got %d", it.Value)
	}
}

// ---------------------------------------------------------------------------
// BindAuto tests
// ---------------------------------------------------------------------------

func TestContext_BindAuto_JSON(t *testing.T) {
	body := `{"name":"auto"}`
	req := httptest.NewRequest("POST", "/test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c := NewContext(w, req)

	var data struct {
		Name string `json:"name"`
	}
	if err := c.BindAuto(&data); err != nil {
		t.Fatalf("BindAuto JSON error: %v", err)
	}
	if data.Name != "auto" {
		t.Errorf("expected Name=auto, got %s", data.Name)
	}
}

func TestContext_BindAuto_XML(t *testing.T) {
	type item struct {
		XMLName xml.Name `xml:"item"`
		Name    string   `xml:"name"`
	}
	body := `<item><name>xmlauto</name></item>`
	req := httptest.NewRequest("POST", "/test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	c := NewContext(w, req)

	var it item
	if err := c.BindAuto(&it); err != nil {
		t.Fatalf("BindAuto XML error: %v", err)
	}
	if it.Name != "xmlauto" {
		t.Errorf("expected Name=xmlauto, got %s", it.Name)
	}
}

func TestContext_BindAuto_TextXML(t *testing.T) {
	type item struct {
		XMLName xml.Name `xml:"item"`
		Name    string   `xml:"name"`
	}
	body := `<item><name>textxml</name></item>`
	req := httptest.NewRequest("POST", "/test", strings.NewReader(body))
	req.Header.Set("Content-Type", "text/xml")
	w := httptest.NewRecorder()
	c := NewContext(w, req)

	var it item
	if err := c.BindAuto(&it); err != nil {
		t.Fatalf("BindAuto text/xml error: %v", err)
	}
	if it.Name != "textxml" {
		t.Errorf("expected Name=textxml, got %s", it.Name)
	}
}

func TestContext_BindAuto_Form(t *testing.T) {
	type formData struct {
		Name string `form:"name"`
	}
	body := strings.NewReader("name=formauto")
	req := httptest.NewRequest("POST", "/test", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	c := NewContext(w, req)

	var fd formData
	if err := c.BindAuto(&fd); err != nil {
		t.Fatalf("BindAuto form error: %v", err)
	}
	if fd.Name != "formauto" {
		t.Errorf("expected Name=formauto, got %s", fd.Name)
	}
}

func TestContext_BindAuto_FallbackJSON(t *testing.T) {
	body := `{"name":"fallback"}`
	req := httptest.NewRequest("POST", "/test", strings.NewReader(body))
	// No content-type header → falls back to JSON
	w := httptest.NewRecorder()
	c := NewContext(w, req)

	var data struct {
		Name string `json:"name"`
	}
	if err := c.BindAuto(&data); err != nil {
		t.Fatalf("BindAuto fallback error: %v", err)
	}
	if data.Name != "fallback" {
		t.Errorf("expected Name=fallback, got %s", data.Name)
	}
}

// ---------------------------------------------------------------------------
// BindValid tests
// ---------------------------------------------------------------------------

type validatableStruct struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

func (v validatableStruct) ValidationRules() validation.Rules {
	return validation.Rules{
		"name":  {"required"},
		"email": {"required", "email"},
	}
}

func TestContext_BindValid_NoServices_Panics(t *testing.T) {
	body := `{"name":"John","email":"john@example.com"}`
	req := httptest.NewRequest("POST", "/test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c := NewContext(w, req)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic when validator service not configured")
		}
	}()

	var data validatableStruct
	c.BindValid(&data)
}

func TestContext_BindValid_WithValidator(t *testing.T) {
	body := `{"name":"John","email":"john@example.com"}`
	req := httptest.NewRequest("POST", "/test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c := NewContext(w, req)
	c.services = &app.Services{Validator: validation.NewValidator()}

	var data validatableStruct
	if err := c.BindValid(&data); err != nil {
		t.Fatalf("BindValid error: %v", err)
	}
}

func TestContext_BindValid_ValidationFails(t *testing.T) {
	body := `{"name":"","email":"notanemail"}`
	req := httptest.NewRequest("POST", "/test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c := NewContext(w, req)
	c.services = &app.Services{Validator: validation.NewValidator()}

	var data validatableStruct
	if err := c.BindValid(&data); err == nil {
		t.Error("expected validation error")
	}
}

func TestContext_BindValid_NotValidatable(t *testing.T) {
	body := `{"name":"John"}`
	req := httptest.NewRequest("POST", "/test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c := NewContext(w, req)
	c.services = &app.Services{Validator: validation.NewValidator()}

	var data struct {
		Name string `json:"name"`
	}
	if err := c.BindValid(&data); err != nil {
		t.Fatalf("BindValid (not validatable) error: %v", err)
	}
}

func TestContext_BindValid_BadJSON(t *testing.T) {
	body := `{bad json}`
	req := httptest.NewRequest("POST", "/test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c := NewContext(w, req)

	var data validatableStruct
	if err := c.BindValid(&data); err == nil {
		t.Error("expected JSON parse error")
	}
}

// ---------------------------------------------------------------------------
// XML response tests
// ---------------------------------------------------------------------------

func TestContext_XML(t *testing.T) {
	type item struct {
		XMLName xml.Name `xml:"item"`
		Name    string   `xml:"name"`
		Value   int      `xml:"value"`
	}

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)

	err := c.XML(http.StatusOK, item{Name: "test", Value: 42})
	if err != nil {
		t.Fatalf("XML error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if w.Header().Get("Content-Type") != "application/xml; charset=utf-8" {
		t.Errorf("expected XML content type, got %s", w.Header().Get("Content-Type"))
	}
	if !strings.Contains(w.Body.String(), "<name>test</name>") {
		t.Errorf("expected XML body to contain <name>test</name>, got %s", w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// File response tests
// ---------------------------------------------------------------------------

// chdirTempForFile creates a temp dir, chdirs into it, and writes the named
// file with the given content. It returns the file's relative name and an
// open *os.Root rooted at the temp dir. The cwd and the open root are
// restored / closed on test cleanup. Tests that exercise File / Download /
// SaveFile need a non-nil *os.Root on the context, the chdir is preserved
// to keep test ergonomics close to the original "relative paths against
// CWD" mental model.
func chdirTempForFile(t *testing.T, name string, content []byte) (string, *os.Root) {
	t.Helper()
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, name), content, 0644); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	root, err := os.OpenRoot(tmp)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return name, root
}

// openTestRoot opens an *os.Root for the given dir and registers cleanup.
func openTestRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func TestContext_File(t *testing.T) {
	fpath, root := chdirTempForFile(t, "test.txt", []byte("hello file"))

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)
	c.fileRoot = root

	if err := c.File(fpath); err != nil {
		t.Fatalf("File error: %v", err)
	}
	if !strings.Contains(w.Body.String(), "hello file") {
		t.Error("expected file content in body")
	}
}

func TestContext_File_DotSlashPrefix(t *testing.T) {
	_, root := chdirTempForFile(t, "ok.txt", []byte("ok"))

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)
	c.fileRoot = root

	if err := c.File("./ok.txt"); err != nil {
		t.Fatalf("File error: %v", err)
	}
	if !strings.Contains(w.Body.String(), "ok") {
		t.Error("expected file content in body")
	}
}

func TestContext_File_Traversal(t *testing.T) {
	root := openTestRoot(t, t.TempDir())
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)
	c.fileRoot = root

	if err := c.File("../../etc/passwd"); err == nil {
		t.Error("expected error for path traversal")
	}
}

func TestContext_File_Absolute(t *testing.T) {
	root := openTestRoot(t, t.TempDir())
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)
	c.fileRoot = root

	if err := c.File("/etc/passwd"); err == nil {
		t.Error("expected error for absolute path")
	}
}

func TestContext_File_NullByte(t *testing.T) {
	root := openTestRoot(t, t.TempDir())
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)
	c.fileRoot = root

	if err := c.File("foo\x00bar"); err == nil {
		t.Error("expected error for null byte in path")
	}
}

func TestContext_File_NilRoot(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)
	// c.fileRoot is intentionally nil to exercise the unconfigured path.

	if err := c.File("anything.txt"); err == nil {
		t.Error("expected error when fileRoot is nil")
	}
}

func TestContext_Download(t *testing.T) {
	fpath, root := chdirTempForFile(t, "dl.txt", []byte("download me"))

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)
	c.fileRoot = root

	if err := c.Download(fpath, "myfile.txt"); err != nil {
		t.Fatalf("Download error: %v", err)
	}
	disp := w.Header().Get("Content-Disposition")
	if !strings.Contains(disp, "myfile.txt") {
		t.Errorf("expected Content-Disposition to contain myfile.txt, got %s", disp)
	}
	if !strings.Contains(w.Body.String(), "download me") {
		t.Error("expected file content in body")
	}
}

func TestContext_Download_Traversal(t *testing.T) {
	root := openTestRoot(t, t.TempDir())
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)
	c.fileRoot = root

	if err := c.Download("../../../etc/passwd", "passwd"); err == nil {
		t.Error("expected error for path traversal")
	}
}

func TestContext_Download_Absolute(t *testing.T) {
	root := openTestRoot(t, t.TempDir())
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)
	c.fileRoot = root

	if err := c.Download("/etc/passwd", "passwd"); err == nil {
		t.Error("expected error for absolute path")
	}
}

func TestContext_Download_NullByte(t *testing.T) {
	root := openTestRoot(t, t.TempDir())
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)
	c.fileRoot = root

	if err := c.Download("foo\x00bar", "file.txt"); err == nil {
		t.Error("expected error for null byte in path")
	}
}

func TestContext_Attachment(t *testing.T) {
	fpath, root := chdirTempForFile(t, "att.txt", []byte("attachment"))

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)
	c.fileRoot = root

	if err := c.Attachment(fpath, "att.txt"); err != nil {
		t.Fatalf("Attachment error: %v", err)
	}
	disp := w.Header().Get("Content-Disposition")
	if !strings.Contains(disp, "att.txt") {
		t.Errorf("expected Content-Disposition to contain att.txt, got %s", disp)
	}
}

// ---------------------------------------------------------------------------
// SSE tests
// ---------------------------------------------------------------------------

func TestContext_SSE(t *testing.T) {
	req := httptest.NewRequest("GET", "/events", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)

	// First call should set headers
	if err := c.SSE("message", map[string]string{"text": "hello"}); err != nil {
		t.Fatalf("SSE error: %v", err)
	}
	if w.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("expected text/event-stream, got %s", w.Header().Get("Content-Type"))
	}
	if w.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("expected no-cache, got %s", w.Header().Get("Cache-Control"))
	}
	// Connection: keep-alive is intentionally NOT emitted; it is the HTTP/1.1
	// default and forbidden hop-by-hop in HTTP/2 (RFC 7540 §8.1.2.2).
	if got := w.Header().Get("Connection"); got != "" {
		t.Errorf("Connection header should be unset, got %q", got)
	}
	// X-Accel-Buffering: no disables proxy buffering so frames are pushed
	// to the client immediately under reverse proxies that honor it.
	if got := w.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Errorf("expected X-Accel-Buffering=no, got %q", got)
	}

	body := w.Body.String()
	if !strings.Contains(body, "event: message") {
		t.Error("expected event line in body")
	}
	if !strings.Contains(body, `"text":"hello"`) {
		t.Error("expected JSON data in body")
	}
}

func TestContext_PrepareStream(t *testing.T) {
	req := httptest.NewRequest("GET", "/stream", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)

	c.PrepareStream()
	if got := w.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type=%q, want text/event-stream", got)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control=%q, want no-cache", got)
	}
	if got := w.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Errorf("X-Accel-Buffering=%q, want no", got)
	}
	if got := w.Header().Get("Connection"); got != "" {
		t.Errorf("Connection=%q, want empty (forbidden in HTTP/2)", got)
	}

	// Idempotent: a second call must not change anything or panic.
	c.PrepareStream()
	if got := w.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("after second call, Content-Type=%q", got)
	}

	// SSE called after PrepareStream must NOT re-set headers (sseStarted
	// already true) and must successfully append a frame.
	if err := c.SSE("hello", "world"); err != nil {
		t.Fatalf("SSE after PrepareStream errored: %v", err)
	}
	if !strings.Contains(w.Body.String(), "event: hello") {
		t.Errorf("frame missing from body: %q", w.Body.String())
	}
}

func TestContext_SSE_MultipleEvents(t *testing.T) {
	req := httptest.NewRequest("GET", "/events", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)

	c.SSE("open", "connected")
	c.SSE("update", map[string]int{"count": 1})

	body := w.Body.String()
	if strings.Count(body, "event: ") != 2 {
		t.Errorf("expected 2 events, got body: %s", body)
	}
}

// ---------------------------------------------------------------------------
// FormValue tests
// ---------------------------------------------------------------------------

func TestContext_FormValue(t *testing.T) {
	body := strings.NewReader("email=test@example.com")
	req := httptest.NewRequest("POST", "/test", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	c := NewContext(w, req)

	if c.FormValue("email") != "test@example.com" {
		t.Errorf("expected test@example.com, got %s", c.FormValue("email"))
	}
	if c.FormValue("missing") != "" {
		t.Error("expected empty for missing key")
	}
}

// ---------------------------------------------------------------------------
// FormFile tests
// ---------------------------------------------------------------------------

func TestContext_FormFile(t *testing.T) {
	// Build multipart form
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("upload", "test.txt")
	if err != nil {
		t.Fatal(err)
	}
	fw.Write([]byte("file contents"))
	mw.Close()

	req := httptest.NewRequest("POST", "/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	c := NewContext(w, req)

	fh, err := c.FormFile("upload")
	if err != nil {
		t.Fatalf("FormFile error: %v", err)
	}
	if fh.Filename != "test.txt" {
		t.Errorf("expected filename test.txt, got %s", fh.Filename)
	}
}

func TestContext_FormFile_Missing(t *testing.T) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.Close()

	req := httptest.NewRequest("POST", "/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	c := NewContext(w, req)

	_, err := c.FormFile("nonexistent")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

// ---------------------------------------------------------------------------
// SaveFile tests
// ---------------------------------------------------------------------------

// newUploadContext builds a *Context carrying a single multipart upload named
// "upload" with the given filename and content.
func newUploadContext(t *testing.T, filename string, content []byte) (*Context, *multipart.FileHeader) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("upload", filename)
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(content)
	mw.Close()

	req := httptest.NewRequest("POST", "/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	c := NewContext(w, req)

	fh, err := c.FormFile("upload")
	if err != nil {
		t.Fatal(err)
	}
	return c, fh
}

func TestContext_SaveFile(t *testing.T) {
	c, fh := newUploadContext(t, "test.txt", []byte("save me"))
	tmp := t.TempDir()
	c.fileRoot = openTestRoot(t, tmp)

	dst := "saved.txt"
	if err := c.SaveFile(fh, dst); err != nil {
		t.Fatalf("SaveFile error: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(tmp, dst))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "save me" {
		t.Errorf("expected 'save me', got '%s'", string(content))
	}
}

func TestContext_SaveFile_DotSlashPrefix(t *testing.T) {
	c, fh := newUploadContext(t, "test.txt", []byte("dot slash"))
	tmp := t.TempDir()
	c.fileRoot = openTestRoot(t, tmp)

	if err := c.SaveFile(fh, "./saved.txt"); err != nil {
		t.Fatalf("SaveFile error: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(tmp, "saved.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "dot slash" {
		t.Errorf("expected 'dot slash', got %q", string(content))
	}
}

func TestContext_SaveFile_Traversal(t *testing.T) {
	c, fh := newUploadContext(t, "test.txt", []byte("data"))
	c.fileRoot = openTestRoot(t, t.TempDir())
	if err := c.SaveFile(fh, "../../../tmp/evil.txt"); err == nil {
		t.Error("expected error for path traversal")
	}
}

func TestContext_SaveFile_Absolute(t *testing.T) {
	c, fh := newUploadContext(t, "test.txt", []byte("data"))
	c.fileRoot = openTestRoot(t, t.TempDir())
	if err := c.SaveFile(fh, "/etc/passwd"); err == nil {
		t.Error("expected error for absolute path")
	}
}

func TestContext_SaveFile_NullByte(t *testing.T) {
	c, fh := newUploadContext(t, "test.txt", []byte("data"))
	c.fileRoot = openTestRoot(t, t.TempDir())
	if err := c.SaveFile(fh, "foo\x00bar"); err == nil {
		t.Error("expected error for null byte in path")
	}
}

// ---------------------------------------------------------------------------
// FileRoot containment tests
//
// These tests pin the symlink-resolved containment guard introduced by
// Router.SetFileRoot. Without root resolution, a "relative" path could
// still escape via a symlink whose target points anywhere on disk; the
// validator must catch that even when the textual path looks safe.
// ---------------------------------------------------------------------------

// realPath returns the symlink-resolved absolute form of dir. The tests
// need this because t.TempDir on macOS hands out paths under /var which
// is itself a symlink to /private/var, so an unresolved path would not
// match the validator's containment check.
func realPath(t *testing.T, dir string) string {
	t.Helper()
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestContext_File_FileRoot_Allows(t *testing.T) {
	dir := realPath(t, t.TempDir())
	if err := os.WriteFile(filepath.Join(dir, "ok.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)
	c.fileRoot = openTestRoot(t, dir)

	if err := c.File("ok.txt"); err != nil {
		t.Fatalf("File error: %v", err)
	}
	if !strings.Contains(w.Body.String(), "ok") {
		t.Errorf("expected file body, got %q", w.Body.String())
	}
}

func TestContext_File_FileRoot_RejectsSymlinkEscape(t *testing.T) {
	dir := realPath(t, t.TempDir())
	outside := realPath(t, t.TempDir())
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A symlink inside the root whose target is the sibling temp dir.
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(dir, "escape.txt")); err != nil {
		t.Skipf("symlink not supported on this platform: %v", err)
	}
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)
	c.fileRoot = openTestRoot(t, dir)

	if err := c.File("escape.txt"); err == nil {
		t.Errorf("expected error for symlink escape, body=%q", w.Body.String())
	}
}

// TestContext_File_FileRoot_TOCTOU_SymlinkSwap pins the kernel-enforced
// containment behaviour: even if an attacker swaps an in-root regular
// file for a symlink-to-outside between path validation and the actual
// open, *os.Root rejects the open. The Lstat-then-Open predecessor had
// a window where the swap could win.
func TestContext_File_FileRoot_TOCTOU_SymlinkSwap(t *testing.T) {
	dir := realPath(t, t.TempDir())
	outside := realPath(t, t.TempDir())
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "swap.txt")
	if err := os.WriteFile(target, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)
	c.fileRoot = openTestRoot(t, dir)

	// Sanity check: the regular file inside the root is served.
	if err := c.File("swap.txt"); err != nil {
		t.Fatalf("File error before swap: %v", err)
	}

	// Simulate the TOCTOU swap: regular file becomes a symlink to an
	// out-of-root location. With os.Root, the next open MUST refuse.
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), target); err != nil {
		t.Skipf("symlink not supported on this platform: %v", err)
	}

	w2 := httptest.NewRecorder()
	c2 := NewContext(w2, req)
	c2.fileRoot = c.fileRoot
	if err := c2.File("swap.txt"); err == nil {
		t.Errorf("post-swap open should fail, body=%q", w2.Body.String())
	}
	if strings.Contains(w2.Body.String(), "secret") {
		t.Errorf("os.Root let the symlinked secret through: %q", w2.Body.String())
	}
}

func TestContext_File_FileRoot_RejectsTraversal(t *testing.T) {
	dir := realPath(t, t.TempDir())
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)
	c.fileRoot = openTestRoot(t, dir)

	if err := c.File("../etc/passwd"); err == nil {
		t.Error("expected error for ../ traversal")
	}
}

func TestContext_File_FileRoot_RejectsAbsolute(t *testing.T) {
	dir := realPath(t, t.TempDir())
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)
	c.fileRoot = openTestRoot(t, dir)

	if err := c.File("/etc/passwd"); err == nil {
		t.Error("expected error for absolute path")
	}
}

func TestContext_SaveFile_FileRoot_AllowsNonExistent(t *testing.T) {
	dir := realPath(t, t.TempDir())
	c, fh := newUploadContext(t, "test.txt", []byte("new"))
	c.fileRoot = openTestRoot(t, dir)

	if err := c.SaveFile(fh, "subdir-does-not-exist-yet.txt"); err != nil {
		t.Fatalf("SaveFile error: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "subdir-does-not-exist-yet.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("expected 'new', got %q", string(got))
	}
}

func TestContext_SaveFile_FileRoot_RejectsParentSymlinkEscape(t *testing.T) {
	dir := realPath(t, t.TempDir())
	outside := realPath(t, t.TempDir())
	// Symlink "uploads" inside the root to a directory outside the root.
	// A SaveFile to "uploads/new.txt" would, without kernel-enforced
	// containment, silently write into the attacker-controlled outside dir.
	if err := os.Symlink(outside, filepath.Join(dir, "uploads")); err != nil {
		t.Skipf("symlink not supported on this platform: %v", err)
	}
	c, fh := newUploadContext(t, "test.txt", []byte("data"))
	c.fileRoot = openTestRoot(t, dir)

	if err := c.SaveFile(fh, "uploads/new.txt"); err == nil {
		t.Error("expected error for SaveFile through symlinked parent")
	}
	// And confirm nothing actually landed in the outside dir.
	if _, statErr := os.Stat(filepath.Join(outside, "new.txt")); statErr == nil {
		t.Error("SaveFile wrote to outside dir despite symlink escape")
	}
}

// ---------------------------------------------------------------------------
// DeleteCookie tests
// ---------------------------------------------------------------------------

func TestContext_DeleteCookie(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)

	c.DeleteCookie("session")

	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	ck := cookies[0]
	if ck.Name != "session" {
		t.Errorf("expected cookie name=session, got %s", ck.Name)
	}
	if ck.MaxAge != -1 {
		t.Errorf("expected MaxAge=-1, got %d", ck.MaxAge)
	}
	if ck.Value != "" {
		t.Errorf("expected empty value, got %s", ck.Value)
	}
	if ck.Path != "/" {
		t.Errorf("expected path=/, got %s", ck.Path)
	}
	if !ck.HttpOnly {
		t.Error("expected HttpOnly=true")
	}
}

// ---------------------------------------------------------------------------
// Accepts tests
// ---------------------------------------------------------------------------

func TestContext_Accepts(t *testing.T) {
	tests := []struct {
		name     string
		accept   string
		offered  []string
		expected string
	}{
		{
			name:     "simple match",
			accept:   "application/json",
			offered:  []string{"text/html", "application/json"},
			expected: "application/json",
		},
		{
			name:     "quality value ordering",
			accept:   "text/html;q=0.9, application/json;q=1.0",
			offered:  []string{"text/html", "application/json"},
			expected: "application/json",
		},
		{
			name:     "wildcard",
			accept:   "*/*",
			offered:  []string{"application/json"},
			expected: "application/json",
		},
		{
			name:     "no match",
			accept:   "text/html",
			offered:  []string{"application/json"},
			expected: "",
		},
		{
			name:     "empty accept returns first offered",
			accept:   "",
			offered:  []string{"application/json", "text/html"},
			expected: "application/json",
		},
		{
			name:     "no offered",
			accept:   "text/html",
			offered:  []string{},
			expected: "",
		},
		{
			name:     "complex quality values",
			accept:   "text/html;q=0.5, application/xml;q=0.8, application/json;q=1.0",
			offered:  []string{"text/html", "application/xml", "application/json"},
			expected: "application/json",
		},
		{
			name:     "default quality is 1.0",
			accept:   "text/html, application/json;q=0.9",
			offered:  []string{"application/json", "text/html"},
			expected: "text/html",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test", nil)
			if tt.accept != "" {
				req.Header.Set("Accept", tt.accept)
			}
			w := httptest.NewRecorder()
			c := NewContext(w, req)

			got := c.Accepts(tt.offered...)
			if got != tt.expected {
				t.Errorf("Accepts(%v) = %q, want %q", tt.offered, got, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Can / Cannot / Authorize tests
// ---------------------------------------------------------------------------

// mockAuthGateChecker satisfies the contract.AuthManager interface used by Context
// without importing pkg/auth.
type mockAuthGateChecker struct {
	allows map[string]bool
}

func (m *mockAuthGateChecker) GateAllows(r *http.Request, ability string, args ...interface{}) bool {
	if m.allows == nil {
		return false
	}
	return m.allows[ability]
}

func (m *mockAuthGateChecker) GateAuthorize(r *http.Request, ability string, args ...interface{}) error {
	if !m.GateAllows(r, ability, args...) {
		return fmt.Errorf("unauthorized action")
	}
	return nil
}

func TestContext_Can(t *testing.T) {
	tests := []struct {
		name    string
		auth    contract.AuthManager
		ability string
		want    bool
	}{
		{
			name:    "allowed ability",
			auth:    &mockAuthGateChecker{allows: map[string]bool{"edit": true}},
			ability: "edit",
			want:    true,
		},
		{
			name:    "denied ability",
			auth:    &mockAuthGateChecker{allows: map[string]bool{"edit": false}},
			ability: "edit",
			want:    false,
		},
		{
			name:    "undefined ability",
			auth:    &mockAuthGateChecker{allows: map[string]bool{}},
			ability: "delete",
			want:    false,
		},
		{
			name:    "nil auth",
			auth:    nil,
			ability: "edit",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			w := httptest.NewRecorder()
			c := NewContext(w, req)
			c.services = &app.Services{Auth: tt.auth}

			got := c.Can(tt.ability)
			if got != tt.want {
				t.Errorf("Can(%q) = %v, want %v", tt.ability, got, tt.want)
			}
		})
	}
}

func TestContext_Can_NoServices(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)
	// services is nil by default

	if c.Can("anything") {
		t.Error("Can() should return false when services is nil")
	}
}

func TestContext_Cannot(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)
	c.services = &app.Services{
		Auth: &mockAuthGateChecker{allows: map[string]bool{"edit": true, "delete": false}},
	}

	if c.Cannot("edit") {
		t.Error("Cannot('edit') should return false for allowed ability")
	}

	if !c.Cannot("delete") {
		t.Error("Cannot('delete') should return true for denied ability")
	}

	if !c.Cannot("unknown") {
		t.Error("Cannot('unknown') should return true for undefined ability")
	}
}

func TestContext_Authorize(t *testing.T) {
	tests := []struct {
		name     string
		services *app.Services
		ability  string
		wantErr  bool
		wantCode int
	}{
		{
			name:     "allowed ability returns nil",
			services: &app.Services{Auth: &mockAuthGateChecker{allows: map[string]bool{"edit": true}}},
			ability:  "edit",
			wantErr:  false,
		},
		{
			name:     "denied ability returns 403",
			services: &app.Services{Auth: &mockAuthGateChecker{allows: map[string]bool{"delete": false}}},
			ability:  "delete",
			wantErr:  true,
			wantCode: http.StatusForbidden,
		},
		{
			name:     "nil auth returns 403",
			services: &app.Services{Auth: nil},
			ability:  "anything",
			wantErr:  true,
			wantCode: http.StatusForbidden,
		},
		{
			name:     "nil services returns 403",
			services: nil,
			ability:  "anything",
			wantErr:  true,
			wantCode: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			w := httptest.NewRecorder()
			c := NewContext(w, req)
			c.services = tt.services

			err := c.Authorize(tt.ability)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Authorize(%q) error = %v, wantErr %v", tt.ability, err, tt.wantErr)
			}
			if tt.wantCode != 0 {
				httpErr, ok := err.(*HTTPError)
				if !ok {
					t.Fatalf("expected *HTTPError, got %T", err)
				}
				if httpErr.Code != tt.wantCode {
					t.Errorf("expected status %d, got %d", tt.wantCode, httpErr.Code)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Resource test (end-to-end with pkg/resource)
// ---------------------------------------------------------------------------

// userResource wraps a user model and implements resource.Resource with
// conditional fields, exercising the full transformation pipeline.
type userResource struct {
	ID      int
	Name    string
	Email   string
	IsAdmin bool
}

func (u *userResource) ToResource() map[string]any {
	m := map[string]any{
		"id":   u.ID,
		"name": u.Name,
	}
	resource.Merge(m, func(m map[string]any) {
		if k, v, ok := resource.When(u.IsAdmin, "email", u.Email); ok {
			m[k] = v
		}
	})
	return m
}

func TestContext_Resource(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)

	r := &userResource{ID: 1, Name: "Alice", Email: "alice@example.com", IsAdmin: true}
	if err := c.Resource(r); err != nil {
		t.Fatalf("Resource() error: %v", err)
	}

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	// Verify ToResource transformation ran (not just JSON passthrough)
	if result["id"] != float64(1) {
		t.Errorf("expected id=1, got %v", result["id"])
	}
	if result["email"] != "alice@example.com" {
		t.Errorf("expected email included for admin, got %v", result["email"])
	}

	// Verify conditional exclusion: non-admin should omit email
	w2 := httptest.NewRecorder()
	c2 := NewContext(w2, httptest.NewRequest("GET", "/", nil))
	r2 := &userResource{ID: 2, Name: "Bob", Email: "bob@example.com", IsAdmin: false}
	c2.Resource(r2)

	var result2 map[string]any
	json.Unmarshal(w2.Body.Bytes(), &result2)
	if _, exists := result2["email"]; exists {
		t.Error("expected email excluded for non-admin")
	}
}

// TestContext_reset_clearsAllFields is a regression gate against pooled
// contexts leaking state from one request into the next (Gin CVE-2020-28483
// shape). If a new field is added to Context, this test will fail unless
// reset() clears it; add the corresponding assertion below.
func TestContext_reset_clearsAllFields(t *testing.T) {
	req := httptest.NewRequest("GET", "/sentinel", nil)
	w := httptest.NewRecorder()

	trusted, _ := ParseTrustedProxies([]string{"10.0.0.0/8"})
	c := &Context{
		Response:       w,
		Request:        req,
		params:         []RouteParam{{Key: "id", Value: "42"}},
		values:         map[string]interface{}{"sentinel": "must-not-leak"},
		services:       &app.Services{},
		sseStarted:     true,
		trustedProxies: trusted,
		validateFn:     func(c *Context, rules map[string][]string, messages ...map[string]string) error { return nil },
	}

	c.reset()

	if c.Response != nil {
		t.Error("reset did not clear Response")
	}
	if c.Request != nil {
		t.Error("reset did not clear Request")
	}
	if len(c.params) != 0 {
		t.Errorf("reset did not clear params: got %d entries", len(c.params))
	}
	if len(c.values) != 0 {
		t.Errorf("reset did not clear values: got %d entries", len(c.values))
	}
	if _, leaked := c.values["sentinel"]; leaked {
		t.Error("sentinel key leaked across reset, pool reuse is unsafe")
	}
	if c.services != nil {
		t.Error("reset did not clear services")
	}
	if c.sseStarted {
		t.Error("reset did not clear sseStarted")
	}
	if c.trustedProxies != nil {
		t.Error("reset did not clear trustedProxies")
	}
	if c.validateFn != nil {
		t.Error("reset did not clear validateFn")
	}
}
