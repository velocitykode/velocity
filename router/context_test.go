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
		"name":  "required",
		"email": "required|email",
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

func TestContext_File(t *testing.T) {
	// Create a temp file
	tmp := t.TempDir()
	fpath := filepath.Join(tmp, "test.txt")
	if err := os.WriteFile(fpath, []byte("hello file"), 0644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)

	if err := c.File(fpath); err != nil {
		t.Fatalf("File error: %v", err)
	}
	if !strings.Contains(w.Body.String(), "hello file") {
		t.Error("expected file content in body")
	}
}

func TestContext_File_Traversal(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)

	if err := c.File("../../etc/passwd"); err == nil {
		t.Error("expected error for path traversal")
	}
}

func TestContext_Download(t *testing.T) {
	tmp := t.TempDir()
	fpath := filepath.Join(tmp, "dl.txt")
	if err := os.WriteFile(fpath, []byte("download me"), 0644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)

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
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)

	if err := c.Download("../../../etc/passwd", "passwd"); err == nil {
		t.Error("expected error for path traversal")
	}
}

func TestContext_Attachment(t *testing.T) {
	tmp := t.TempDir()
	fpath := filepath.Join(tmp, "att.txt")
	if err := os.WriteFile(fpath, []byte("attachment"), 0644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)

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
	if w.Header().Get("Connection") != "keep-alive" {
		t.Errorf("expected keep-alive, got %s", w.Header().Get("Connection"))
	}

	body := w.Body.String()
	if !strings.Contains(body, "event: message") {
		t.Error("expected event line in body")
	}
	if !strings.Contains(body, `"text":"hello"`) {
		t.Error("expected JSON data in body")
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

func TestContext_SaveFile(t *testing.T) {
	// Build multipart form
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("upload", "test.txt")
	if err != nil {
		t.Fatal(err)
	}
	fw.Write([]byte("save me"))
	mw.Close()

	req := httptest.NewRequest("POST", "/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	c := NewContext(w, req)

	fh, err := c.FormFile("upload")
	if err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "saved.txt")
	if err := c.SaveFile(fh, dst); err != nil {
		t.Fatalf("SaveFile error: %v", err)
	}

	content, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "save me" {
		t.Errorf("expected 'save me', got '%s'", string(content))
	}
}

func TestContext_SaveFile_Traversal(t *testing.T) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("upload", "test.txt")
	fw.Write([]byte("data"))
	mw.Close()

	req := httptest.NewRequest("POST", "/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	c := NewContext(w, req)

	fh, _ := c.FormFile("upload")
	if err := c.SaveFile(fh, "../../../tmp/evil.txt"); err == nil {
		t.Error("expected error for path traversal")
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
// reset() clears it — add the corresponding assertion below.
func TestContext_reset_clearsAllFields(t *testing.T) {
	req := httptest.NewRequest("GET", "/sentinel", nil)
	w := httptest.NewRecorder()

	c := &Context{
		Response:       w,
		Request:        req,
		params:         []RouteParam{{Key: "id", Value: "42"}},
		values:         map[string]interface{}{"sentinel": "must-not-leak"},
		services:       &app.Services{},
		sseStarted:     true,
		trustedProxies: []string{"10.0.0.0/8"},
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
		t.Error("sentinel key leaked across reset — pool reuse is unsafe")
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
