package http

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRouterBasicRouting(t *testing.T) {
	router := NewRouter()

	router.GET("/", func(c *Context) error {
		return c.String(200, "home")
	})

	router.GET("/users", func(c *Context) error {
		return c.JSON(200, map[string]string{"route": "users"})
	})

	router.POST("/users", func(c *Context) error {
		return c.String(201, "user created")
	})

	tests := []struct {
		method       string
		path         string
		expectedCode int
		expectedBody string
	}{
		{"GET", "/", 200, "home"},
		{"GET", "/users", 200, `{"route":"users"}`},
		{"POST", "/users", 201, "user created"},
		{"GET", "/notfound", 404, "404 page not found"},
	}

	for _, tt := range tests {
		req := httptest.NewRequest(tt.method, tt.path, nil)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != tt.expectedCode {
			t.Errorf("Expected status %d, got %d for %s %s", tt.expectedCode, w.Code, tt.method, tt.path)
		}

		body := w.Body.String()
		if tt.expectedCode == 200 || tt.expectedCode == 201 {
			if body != tt.expectedBody && body != tt.expectedBody+"\n" {
				t.Errorf("Expected body '%s', got '%s' for %s %s", tt.expectedBody, body, tt.method, tt.path)
			}
		}
	}
}

func TestRouterParameterExtraction(t *testing.T) {
	router := NewRouter()

	router.GET("/users/{id}", func(c *Context) error {
		id := c.Param("id")
		return c.String(200, "user:%s", id)
	})

	router.GET("/posts/{post_id}/comments/{comment_id}", func(c *Context) error {
		postID := c.Param("post_id")
		commentID := c.Param("comment_id")
		return c.String(200, "post:%s,comment:%s", postID, commentID)
	})

	tests := []struct {
		path         string
		expectedBody string
	}{
		{"/users/123", "user:123"},
		{"/users/abc", "user:abc"},
		{"/posts/10/comments/5", "post:10,comment:5"},
	}

	for _, tt := range tests {
		req := httptest.NewRequest("GET", tt.path, nil)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Errorf("Expected status 200, got %d for %s", w.Code, tt.path)
		}

		body := w.Body.String()
		if body != tt.expectedBody {
			t.Errorf("Expected body '%s', got '%s' for %s", tt.expectedBody, body, tt.path)
		}
	}
}

func TestRouteGroups(t *testing.T) {
	router := NewRouter()

	api := router.Prefix("/api")
	api.GET("/users", func(c *Context) error {
		return c.String(200, "api users")
	})

	v1 := api.Group("/v1")
	v1.GET("/posts", func(c *Context) error {
		return c.String(200, "v1 posts")
	})

	admin := router.Prefix("/admin")
	admin.GET("/dashboard", func(c *Context) error {
		return c.String(200, "admin dashboard")
	})

	tests := []struct {
		path         string
		expectedCode int
		expectedBody string
	}{
		{"/api/users", 200, "api users"},
		{"/api/v1/posts", 200, "v1 posts"},
		{"/admin/dashboard", 200, "admin dashboard"},
		{"/users", 404, ""},
	}

	for _, tt := range tests {
		req := httptest.NewRequest("GET", tt.path, nil)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != tt.expectedCode {
			t.Errorf("Expected status %d, got %d for %s", tt.expectedCode, w.Code, tt.path)
		}

		if tt.expectedCode == 200 {
			body := w.Body.String()
			if body != tt.expectedBody {
				t.Errorf("Expected body '%s', got '%s' for %s", tt.expectedBody, body, tt.path)
			}
		}
	}
}

func TestMiddleware(t *testing.T) {
	router := NewRouter()

	headerMiddleware := func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			c.Writer.Header().Set("X-Test", "middleware")
			return next(c)
		}
	}

	authMiddleware := func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			if c.Query("auth") != "secret" {
				return c.String(401, "unauthorized")
			}
			return next(c)
		}
	}

	router.Middleware(headerMiddleware)

	router.GET("/public", func(c *Context) error {
		return c.String(200, "public")
	})

	router.GET("/protected", func(c *Context) error {
		return c.String(200, "protected")
	}).Middleware(authMiddleware)

	tests := []struct {
		path         string
		expectedCode int
		expectedBody string
		checkHeader  bool
	}{
		{"/public", 200, "public", true},
		{"/protected?auth=secret", 200, "protected", true},
		{"/protected", 401, "unauthorized", true},
	}

	for _, tt := range tests {
		req := httptest.NewRequest("GET", tt.path, nil)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != tt.expectedCode {
			t.Errorf("Expected status %d, got %d for %s", tt.expectedCode, w.Code, tt.path)
		}

		body := w.Body.String()
		if body != tt.expectedBody {
			t.Errorf("Expected body '%s', got '%s' for %s", tt.expectedBody, body, tt.path)
		}

		if tt.checkHeader {
			if w.Header().Get("X-Test") != "middleware" {
				t.Errorf("Expected header X-Test to be 'middleware' for %s", tt.path)
			}
		}
	}
}

func TestNamedRoutes(t *testing.T) {
	router := NewRouter()

	router.GET("/users/{id}", func(c *Context) error {
		return c.String(200, "user")
	}).Name("user.show")

	router.GET("/posts/{id}/edit", func(c *Context) error {
		return c.String(200, "edit")
	}).Name("post.edit")

	tests := []struct {
		name   string
		params map[string]string
		want   string
	}{
		{"user.show", map[string]string{"id": "123"}, "/users/123"},
		{"post.edit", map[string]string{"id": "456"}, "/posts/456/edit"},
	}

	for _, tt := range tests {
		url, err := router.URL(tt.name, tt.params)
		if err != nil {
			t.Errorf("Failed to generate URL for route %s: %v", tt.name, err)
			continue
		}

		if url != tt.want {
			t.Errorf("Expected URL %s, got %s for route %s", tt.want, url, tt.name)
		}
	}

	_, err := router.URL("nonexistent", nil)
	if err == nil {
		t.Error("Expected error for non-existent route name")
	}
}

func TestContextHelpers(t *testing.T) {
	router := NewRouter()

	router.GET("/test", func(c *Context) error {
		query := c.Query("q")
		if query != "" {
			return c.String(200, "query:%s", query)
		}
		return c.String(200, "%s", "no query")
	})

	router.POST("/form", func(c *Context) error {
		name := c.PostForm("name")
		return c.String(200, "name:%s", name)
	})

	t.Run("Query parameters", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test?q=search", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Body.String() != "query:search" {
			t.Errorf("Expected 'query:search', got '%s'", w.Body.String())
		}
	})

	t.Run("Form data", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/form", nil)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.ParseForm()
		req.Form.Set("name", "John")

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Body.String() != "name:John" {
			t.Errorf("Expected 'name:John', got '%s'", w.Body.String())
		}
	})
}

func TestHTTPMethods(t *testing.T) {
	router := NewRouter()

	methods := []string{"GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS", "HEAD"}

	for _, method := range methods {
		m := method
		switch m {
		case "GET":
			router.GET("/test", func(c *Context) error {
				return c.String(200, "%s", m)
			})
		case "POST":
			router.POST("/test", func(c *Context) error {
				return c.String(200, "%s", m)
			})
		case "PUT":
			router.PUT("/test", func(c *Context) error {
				return c.String(200, "%s", m)
			})
		case "DELETE":
			router.DELETE("/test", func(c *Context) error {
				return c.String(200, "%s", m)
			})
		case "PATCH":
			router.PATCH("/test", func(c *Context) error {
				return c.String(200, "%s", m)
			})
		case "OPTIONS":
			router.OPTIONS("/test", func(c *Context) error {
				return c.String(200, "%s", m)
			})
		case "HEAD":
			router.HEAD("/test", func(c *Context) error {
				c.Writer.WriteHeader(200)
				return nil
			})
		}
	}

	for _, method := range methods {
		req := httptest.NewRequest(method, "/test", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Errorf("Expected status 200 for %s, got %d", method, w.Code)
		}

		if method != "HEAD" && w.Body.String() != method {
			t.Errorf("Expected body '%s', got '%s' for %s", method, w.Body.String(), method)
		}
	}
}

func TestTraceIDExtraction(t *testing.T) {
	router := NewRouter()

	router.GET("/test", func(c *Context) error {
		return c.JSON(200, map[string]string{
			"trace_id":   c.TraceID,
			"request_id": c.RequestID,
		})
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Trace-ID", "trace-123")
	req.Header.Set("X-Request-ID", "req-456")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	// Check response headers echo back
	if w.Header().Get("X-Trace-ID") != "trace-123" {
		t.Errorf("Expected X-Trace-ID header 'trace-123', got '%s'", w.Header().Get("X-Trace-ID"))
	}

	if w.Header().Get("X-Request-ID") != "req-456" {
		t.Errorf("Expected X-Request-ID header 'req-456', got '%s'", w.Header().Get("X-Request-ID"))
	}

	// Check JSON response
	body := w.Body.String()
	if !contains(body, `"trace_id":"trace-123"`) {
		t.Errorf("Expected trace_id in response, got '%s'", body)
	}
	if !contains(body, `"request_id":"req-456"`) {
		t.Errorf("Expected request_id in response, got '%s'", body)
	}
}

func TestLocalsStorage(t *testing.T) {
	router := NewRouter()

	middleware := func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			c.SetLocal("user", "john")
			c.SetLocal("role", "admin")
			return next(c)
		}
	}

	router.Middleware(middleware)

	router.GET("/test", func(c *Context) error {
		user := c.Locals("user")
		role, exists := c.GetLocal("role")

		if user != "john" {
			t.Errorf("Expected user 'john', got '%v'", user)
		}

		if !exists || role != "admin" {
			t.Errorf("Expected role 'admin', got '%v' (exists: %v)", role, exists)
		}

		// Test non-existent key
		_, exists = c.GetLocal("nonexistent")
		if exists {
			t.Error("Expected nonexistent key to not exist")
		}

		return c.String(200, "ok")
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
}

func TestParamInt(t *testing.T) {
	router := NewRouter()

	router.GET("/users/{id}", func(c *Context) error {
		id, err := c.ParamInt("id")
		if err != nil {
			return c.BadRequest("Invalid ID")
		}
		return c.JSON(200, map[string]int{"id": id})
	})

	tests := []struct {
		path       string
		expectCode int
		expectID   int
	}{
		{"/users/123", 200, 123},
		{"/users/abc", 400, 0},
		{"/users/0", 200, 0},
	}

	for _, tt := range tests {
		req := httptest.NewRequest("GET", tt.path, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != tt.expectCode {
			t.Errorf("Expected status %d for %s, got %d", tt.expectCode, tt.path, w.Code)
		}
	}
}

func TestQueryIntAndBool(t *testing.T) {
	router := NewRouter()

	router.GET("/test", func(c *Context) error {
		page := c.QueryInt("page", 1)
		limit := c.QueryInt("limit")
		active := c.QueryBool("active")

		return c.JSON(200, map[string]interface{}{
			"page":   page,
			"limit":  limit,
			"active": active,
		})
	})

	tests := []struct {
		query        string
		expectPage   int
		expectLimit  int
		expectActive bool
	}{
		{"?page=5&limit=10&active=true", 5, 10, true},
		{"?page=invalid&limit=20&active=false", 1, 20, false},
		{"", 1, 0, false},
	}

	for _, tt := range tests {
		req := httptest.NewRequest("GET", "/test"+tt.query, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Errorf("Expected status 200, got %d", w.Code)
		}

		body := w.Body.String()
		if !contains(body, fmt.Sprintf(`"page":%d`, tt.expectPage)) {
			t.Errorf("Expected page %d in response, got '%s'", tt.expectPage, body)
		}
	}
}

func TestBindJSON(t *testing.T) {
	router := NewRouter()

	type User struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}

	router.POST("/users", func(c *Context) error {
		var user User
		if err := c.BindJSON(&user); err != nil {
			return c.BadRequest("Invalid JSON")
		}
		return c.JSON(201, user)
	})

	jsonData := `{"name":"John","email":"john@example.com"}`
	req := httptest.NewRequest("POST", "/users", strings.NewReader(jsonData))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != 201 {
		t.Errorf("Expected status 201, got %d", w.Code)
	}

	body := w.Body.String()
	if !contains(body, `"name":"John"`) {
		t.Errorf("Expected name in response, got '%s'", body)
	}
}

func TestCustomError(t *testing.T) {
	router := NewRouter()

	router.GET("/badrequest", func(c *Context) error {
		return c.BadRequest("Bad input")
	})

	router.GET("/unauthorized", func(c *Context) error {
		return c.Unauthorized("Not authorized")
	})

	router.GET("/notfound", func(c *Context) error {
		return c.NotFound("Resource not found")
	})

	tests := []struct {
		path       string
		expectCode int
		expectMsg  string
	}{
		{"/badrequest", 400, "Bad input"},
		{"/unauthorized", 401, "Not authorized"},
		{"/notfound", 404, "Resource not found"},
	}

	for _, tt := range tests {
		req := httptest.NewRequest("GET", tt.path, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != tt.expectCode {
			t.Errorf("Expected status %d for %s, got %d", tt.expectCode, tt.path, w.Code)
		}

		body := w.Body.String()
		if !contains(body, tt.expectMsg) {
			t.Errorf("Expected message '%s' in response, got '%s'", tt.expectMsg, body)
		}

		// Check for error_id in response
		if !contains(body, `"error_id"`) {
			t.Errorf("Expected error_id in response, got '%s'", body)
		}
	}
}

func TestPanicRecovery(t *testing.T) {
	router := NewRouter()

	router.GET("/panic", func(c *Context) error {
		panic("something went wrong")
	})

	req := httptest.NewRequest("GET", "/panic", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != 500 {
		t.Errorf("Expected status 500, got %d", w.Code)
	}

	body := w.Body.String()
	if !contains(body, `"error"`) {
		t.Errorf("Expected error in response, got '%s'", body)
	}
}

func TestCustomErrorHandler(t *testing.T) {
	customHandler := func(c *Context, err error) {
		c.Set("X-Custom-Error", "true")
		c.JSON(599, map[string]string{
			"custom": "error handler",
			"error":  err.Error(),
		})
	}

	router := NewRouter(Config{
		ErrorHandler: customHandler,
	})

	router.GET("/error", func(c *Context) error {
		return fmt.Errorf("test error")
	})

	req := httptest.NewRequest("GET", "/error", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != 599 {
		t.Errorf("Expected status 599, got %d", w.Code)
	}

	if w.Header().Get("X-Custom-Error") != "true" {
		t.Error("Expected X-Custom-Error header to be set")
	}

	body := w.Body.String()
	if !contains(body, `"custom":"error handler"`) {
		t.Errorf("Expected custom error handler response, got '%s'", body)
	}
}

func TestHeaderHelpers(t *testing.T) {
	router := NewRouter()

	router.GET("/headers", func(c *Context) error {
		userAgent := c.Get("User-Agent")
		c.Set("X-Custom", "test-value")
		return c.String(200, "%s", userAgent)
	})

	req := httptest.NewRequest("GET", "/headers", nil)
	req.Header.Set("User-Agent", "TestAgent/1.0")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Header().Get("X-Custom") != "test-value" {
		t.Errorf("Expected X-Custom header 'test-value', got '%s'", w.Header().Get("X-Custom"))
	}

	if w.Body.String() != "TestAgent/1.0" {
		t.Errorf("Expected body 'TestAgent/1.0', got '%s'", w.Body.String())
	}
}

func TestPredefinedErrors(t *testing.T) {
	router := NewRouter()

	router.GET("/predefined", func(c *Context) error {
		return ErrNotFound
	})

	req := httptest.NewRequest("GET", "/predefined", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Errorf("Expected status 404, got %d", w.Code)
	}

	body := w.Body.String()
	if !contains(body, "Not Found") {
		t.Errorf("Expected 'Not Found' in response, got '%s'", body)
	}
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

func TestRouteGroupAllMethods(t *testing.T) {
	router := NewRouter()
	group := router.Prefix("/api")

	group.GET("/get", func(c *Context) error {
		return c.String(200, "GET")
	})
	group.POST("/post", func(c *Context) error {
		return c.String(200, "POST")
	})
	group.PUT("/put", func(c *Context) error {
		return c.String(200, "PUT")
	})
	group.DELETE("/delete", func(c *Context) error {
		return c.String(200, "DELETE")
	})
	group.PATCH("/patch", func(c *Context) error {
		return c.String(200, "PATCH")
	})

	tests := []struct {
		method string
		path   string
		expect string
	}{
		{"GET", "/api/get", "GET"},
		{"POST", "/api/post", "POST"},
		{"PUT", "/api/put", "PUT"},
		{"DELETE", "/api/delete", "DELETE"},
		{"PATCH", "/api/patch", "PATCH"},
	}

	for _, tt := range tests {
		req := httptest.NewRequest(tt.method, tt.path, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Errorf("Expected status 200 for %s %s, got %d", tt.method, tt.path, w.Code)
		}
		if w.Body.String() != tt.expect {
			t.Errorf("Expected body '%s', got '%s'", tt.expect, w.Body.String())
		}
	}
}

func TestRouteGroupMiddleware(t *testing.T) {
	router := NewRouter()

	groupMiddleware := func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			c.Set("X-Group", "applied")
			return next(c)
		}
	}

	group := router.Prefix("/api").Middleware(groupMiddleware)
	group.GET("/test", func(c *Context) error {
		return c.String(200, "ok")
	})

	req := httptest.NewRequest("GET", "/api/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Header().Get("X-Group") != "applied" {
		t.Error("Expected group middleware to be applied")
	}
}

func TestHTMLResponse(t *testing.T) {
	router := NewRouter()

	router.GET("/html", func(c *Context) error {
		return c.HTML(200, "<h1>Hello</h1>")
	})

	req := httptest.NewRequest("GET", "/html", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	if w.Header().Get("Content-Type") != "text/html" {
		t.Errorf("Expected Content-Type text/html, got %s", w.Header().Get("Content-Type"))
	}

	if w.Body.String() != "<h1>Hello</h1>" {
		t.Errorf("Expected HTML body, got %s", w.Body.String())
	}
}

func TestRedirect(t *testing.T) {
	router := NewRouter()

	router.GET("/redirect", func(c *Context) error {
		return c.Redirect(302, "/new-location")
	})

	req := httptest.NewRequest("GET", "/redirect", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 302 {
		t.Errorf("Expected status 302, got %d", w.Code)
	}

	if w.Header().Get("Location") != "/new-location" {
		t.Errorf("Expected Location header '/new-location', got %s", w.Header().Get("Location"))
	}
}

func TestStatusChaining(t *testing.T) {
	router := NewRouter()

	router.GET("/status", func(c *Context) error {
		c.Status(201)
		_, err := c.Writer.Write([]byte("created"))
		return err
	})

	req := httptest.NewRequest("GET", "/status", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 201 {
		t.Errorf("Expected status 201, got %d", w.Code)
	}
}

func TestContextGetterSetter(t *testing.T) {
	router := NewRouter()

	router.GET("/context", func(c *Context) error {
		// Get original context
		ctx := c.Context()
		if ctx == nil {
			t.Error("Expected context to not be nil")
		}

		// Set new context
		newCtx := context.WithValue(ctx, "key", "value")
		c.SetContext(newCtx)

		// Verify it was set
		if c.Context().Value("key") != "value" {
			t.Error("Expected context value to be set")
		}

		return c.String(200, "ok")
	})

	req := httptest.NewRequest("GET", "/context", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
}

func TestForbiddenError(t *testing.T) {
	router := NewRouter()

	router.GET("/forbidden", func(c *Context) error {
		return c.Forbidden("Access denied")
	})

	req := httptest.NewRequest("GET", "/forbidden", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 403 {
		t.Errorf("Expected status 403, got %d", w.Code)
	}

	body := w.Body.String()
	if !contains(body, "Access denied") {
		t.Errorf("Expected 'Access denied' in response, got %s", body)
	}
}

func TestInternalServerError(t *testing.T) {
	router := NewRouter()

	router.GET("/error", func(c *Context) error {
		return c.InternalServerError("Something went wrong")
	})

	req := httptest.NewRequest("GET", "/error", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 500 {
		t.Errorf("Expected status 500, got %d", w.Code)
	}

	body := w.Body.String()
	if !contains(body, "Something went wrong") {
		t.Errorf("Expected error message in response, got %s", body)
	}
}

func TestBodyMethod(t *testing.T) {
	router := NewRouter()

	router.POST("/body", func(c *Context) error {
		body, err := c.Body()
		if err != nil {
			return c.BadRequest(err.Error())
		}
		return c.String(200, "%s", string(body))
	})

	jsonData := `{"test":"data"}`
	req := httptest.NewRequest("POST", "/body", strings.NewReader(jsonData))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	if w.Body.String() != jsonData {
		t.Errorf("Expected body '%s', got '%s'", jsonData, w.Body.String())
	}
}

func TestSetLocalUpdate(t *testing.T) {
	router := NewRouter()

	router.GET("/update", func(c *Context) error {
		// Set initial value
		c.SetLocal("key", "value1")

		// Update same key
		c.SetLocal("key", "value2")

		val := c.Locals("key")
		if val != "value2" {
			t.Errorf("Expected 'value2', got '%v'", val)
		}

		return c.String(200, "ok")
	})

	req := httptest.NewRequest("GET", "/update", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
}

func TestBindJSONEmptyBody(t *testing.T) {
	router := NewRouter()

	type Data struct {
		Name string `json:"name"`
	}

	router.POST("/bind", func(c *Context) error {
		var data Data
		if err := c.BindJSON(&data); err != nil {
			return c.BadRequest("Empty body")
		}
		return c.JSON(200, data)
	})

	// Test with nil body
	req := httptest.NewRequest("POST", "/bind", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("Expected status 400, got %d", w.Code)
	}

	body := w.Body.String()
	if !contains(body, "Empty body") {
		t.Errorf("Expected error message, got %s", body)
	}
}

func TestParamIntNotFound(t *testing.T) {
	router := NewRouter()

	router.GET("/test", func(c *Context) error {
		// Try to get non-existent param
		_, err := c.ParamInt("id")
		if err == nil {
			t.Error("Expected error for non-existent param")
		}
		return c.String(200, "ok")
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
}

func TestBodyEmptyRequest(t *testing.T) {
	router := NewRouter()

	router.POST("/body", func(c *Context) error {
		body, err := c.Body()
		// Empty body from httptest returns empty slice, not error
		if err != nil {
			return c.BadRequest(err.Error())
		}
		if len(body) == 0 {
			return c.String(200, "empty")
		}
		return c.String(200, "%s", string(body))
	})

	req := httptest.NewRequest("POST", "/body", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
}

func TestURLGenerationError(t *testing.T) {
	router := NewRouter()

	router.GET("/users/{id}", func(c *Context) error {
		return c.String(200, "ok")
	}).Name("user.show")

	// Test with invalid params (missing required param)
	_, err := router.URL("user.show", map[string]string{})
	if err == nil {
		t.Error("Expected error when generating URL with missing params")
	}
}

func TestBindJSONInvalidData(t *testing.T) {
	router := NewRouter()

	type User struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}

	router.POST("/users", func(c *Context) error {
		var user User
		if err := c.BindJSON(&user); err != nil {
			return c.BadRequest("Invalid JSON")
		}
		return c.JSON(200, user)
	})

	// Send invalid JSON
	invalidJSON := `{"name": "John", "age": "not-a-number"`
	req := httptest.NewRequest("POST", "/users", strings.NewReader(invalidJSON))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("Expected status 400, got %d", w.Code)
	}
}

func TestBindJSONNilBody(t *testing.T) {
	router := NewRouter()

	type User struct {
		Name string `json:"name"`
	}

	router.POST("/users", func(c *Context) error {
		var user User
		if err := c.BindJSON(&user); err != nil {
			return c.BadRequest("Body is nil")
		}
		return c.JSON(200, user)
	})

	req := httptest.NewRequest("POST", "/users", nil)
	req.Body = nil // Explicitly set to nil
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("Expected status 400, got %d", w.Code)
	}
}

func TestBodyNilRequest(t *testing.T) {
	router := NewRouter()

	router.POST("/body", func(c *Context) error {
		_, err := c.Body()
		if err != nil {
			return c.BadRequest("Body is nil")
		}
		return c.String(200, "ok")
	})

	req := httptest.NewRequest("POST", "/body", nil)
	req.Body = nil // Explicitly set to nil
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("Expected status 400, got %d", w.Code)
	}
}
