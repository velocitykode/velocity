package router

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// TestUserController is a test controller with all resource methods
type TestUserController struct {
	called map[string]bool
}

func NewTestUserController() *TestUserController {
	return &TestUserController{called: make(map[string]bool)}
}

func (c *TestUserController) Index(ctx *Context) error {
	c.called["index"] = true
	return ctx.String(http.StatusOK, "index")
}

func (c *TestUserController) Create(ctx *Context) error {
	c.called["create"] = true
	return ctx.String(http.StatusOK, "create")
}

func (c *TestUserController) Store(ctx *Context) error {
	c.called["store"] = true
	return ctx.String(http.StatusCreated, "store")
}

func (c *TestUserController) Show(ctx *Context) error {
	c.called["show"] = true
	id := ctx.Param("id")
	return ctx.String(http.StatusOK, "show:"+id)
}

func (c *TestUserController) Edit(ctx *Context) error {
	c.called["edit"] = true
	return ctx.String(http.StatusOK, "edit")
}

func (c *TestUserController) Update(ctx *Context) error {
	c.called["update"] = true
	return ctx.String(http.StatusOK, "update")
}

func (c *TestUserController) Destroy(ctx *Context) error {
	c.called["destroy"] = true
	return ctx.NoContent()
}

// PartialController only implements some resource methods
type PartialController struct {
	indexCalled bool
	showCalled  bool
}

func (c *PartialController) Index(ctx *Context) error {
	c.indexCalled = true
	return nil
}

func (c *PartialController) Show(ctx *Context) error {
	c.showCalled = true
	return nil
}

func TestResource_FullController(t *testing.T) {
	t.Run("registers all resource routes", func(t *testing.T) {
		router := NewV2()
		controller := NewTestUserController()

		router.Resource("/users", controller)

		tests := []struct {
			method string
			path   string
			action string
		}{
			{"GET", "/users", "index"},
			{"GET", "/users/create", "create"},
			{"POST", "/users", "store"},
			{"GET", "/users/123", "show"},
			{"GET", "/users/123/edit", "edit"},
			{"PUT", "/users/123", "update"},
			{"DELETE", "/users/123", "destroy"},
		}

		for _, tt := range tests {
			controller.called = make(map[string]bool) // Reset

			req := httptest.NewRequest(tt.method, tt.path, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code >= 400 {
				t.Errorf("%s %s: expected success, got %d", tt.method, tt.path, w.Code)
				continue
			}

			if !controller.called[tt.action] {
				t.Errorf("%s %s: expected %s to be called", tt.method, tt.path, tt.action)
			}
		}
	})

	t.Run("Show extracts id parameter", func(t *testing.T) {
		router := NewV2()
		controller := NewTestUserController()
		router.Resource("/users", controller)

		req := httptest.NewRequest("GET", "/users/456", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Body.String() != "show:456" {
			t.Errorf("expected body 'show:456', got %q", w.Body.String())
		}
	})
}

func TestResource_Only(t *testing.T) {
	t.Run("Only includes specified methods", func(t *testing.T) {
		router := NewV2()
		controller := NewTestUserController()

		router.Resource("/posts", controller).Only("index", "show")

		// Index should work
		req := httptest.NewRequest("GET", "/posts", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("GET /posts: expected 200, got %d", w.Code)
		}

		// Show should work
		req = httptest.NewRequest("GET", "/posts/1", nil)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("GET /posts/1: expected 200, got %d", w.Code)
		}

		// Store should NOT work
		req = httptest.NewRequest("POST", "/posts", nil)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("POST /posts: expected 404, got %d", w.Code)
		}

		// Destroy should NOT work
		req = httptest.NewRequest("DELETE", "/posts/1", nil)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("DELETE /posts/1: expected 404, got %d", w.Code)
		}
	})
}

func TestResource_Except(t *testing.T) {
	t.Run("Except excludes specified methods", func(t *testing.T) {
		router := NewV2()
		controller := NewTestUserController()

		router.Resource("/comments", controller).Except("destroy", "store", "edit")

		// Index should work
		req := httptest.NewRequest("GET", "/comments", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("GET /comments: expected 200, got %d", w.Code)
		}

		// Create should work (not excluded)
		req = httptest.NewRequest("GET", "/comments/create", nil)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("GET /comments/create: expected 200, got %d", w.Code)
		}

		// Destroy should NOT work (excluded)
		req = httptest.NewRequest("DELETE", "/comments/1", nil)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("DELETE /comments/1: expected 404, got %d", w.Code)
		}

		// Store should NOT work (excluded)
		req = httptest.NewRequest("POST", "/comments", nil)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("POST /comments: expected 404, got %d", w.Code)
		}
	})
}

func TestResource_PartialController(t *testing.T) {
	t.Run("only registers methods that exist on controller", func(t *testing.T) {
		router := NewV2()
		controller := &PartialController{}

		router.Resource("/items", controller)

		// Index should work (method exists)
		req := httptest.NewRequest("GET", "/items", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("GET /items: expected 200, got %d", w.Code)
		}

		// Show should work (method exists)
		req = httptest.NewRequest("GET", "/items/1", nil)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("GET /items/1: expected 200, got %d", w.Code)
		}

		// Store should NOT work (method doesn't exist)
		req = httptest.NewRequest("POST", "/items", nil)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("POST /items: expected 404, got %d", w.Code)
		}
	})
}

func TestIsValidHandlerSignature(t *testing.T) {
	t.Run("validates correct signature", func(t *testing.T) {
		controller := NewTestUserController()
		controllerType := reflect.TypeOf(controller)

		method, _ := controllerType.MethodByName("Index")
		if !isValidHandlerSignature(method.Type) {
			t.Error("Index should have valid signature")
		}
	})

	t.Run("rejects method with wrong input count", func(t *testing.T) {
		// BadController has methods with wrong signatures
		controller := &BadSignatureController{}
		controllerType := reflect.TypeOf(controller)

		// NoArgs takes no arguments (only receiver)
		method, _ := controllerType.MethodByName("NoArgs")
		if isValidHandlerSignature(method.Type) {
			t.Error("NoArgs should be rejected - missing *Context parameter")
		}
	})

	t.Run("rejects method with wrong output count", func(t *testing.T) {
		controller := &BadSignatureController{}
		controllerType := reflect.TypeOf(controller)

		// NoReturn doesn't return error
		method, _ := controllerType.MethodByName("NoReturn")
		if isValidHandlerSignature(method.Type) {
			t.Error("NoReturn should be rejected - no return value")
		}
	})

	t.Run("rejects method with wrong input type", func(t *testing.T) {
		controller := &BadSignatureController{}
		controllerType := reflect.TypeOf(controller)

		// WrongInput takes string instead of *Context
		method, _ := controllerType.MethodByName("WrongInput")
		if isValidHandlerSignature(method.Type) {
			t.Error("WrongInput should be rejected - wrong parameter type")
		}
	})
}

// BadSignatureController has methods with invalid handler signatures
type BadSignatureController struct{}

func (c *BadSignatureController) NoArgs() error {
	return nil
}

func (c *BadSignatureController) NoReturn(ctx *Context) {
}

func (c *BadSignatureController) WrongInput(s string) error {
	return nil
}

func TestResource_ControllerWithInvalidMethods(t *testing.T) {
	t.Run("skips methods with invalid signatures", func(t *testing.T) {
		router := NewV2()
		controller := &BadSignatureController{}

		// Should not panic, just skip invalid methods
		router.Resource("/bad", controller)

		req := httptest.NewRequest("GET", "/bad", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// Should return 404 because no valid methods registered
		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d", w.Code)
		}
	})
}

func TestResource_HandlerReturnsError(t *testing.T) {
	t.Run("propagates error from controller method", func(t *testing.T) {
		router := NewV2()
		controller := &ErrorController{}

		router.Resource("/errors", controller)

		req := httptest.NewRequest("GET", "/errors", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusInternalServerError {
			t.Errorf("expected 500, got %d", w.Code)
		}
	})
}

type ErrorController struct{}

func (c *ErrorController) Index(ctx *Context) error {
	return http.ErrBodyNotAllowed // Return an error
}

func TestResource_DoubleRegistration(t *testing.T) {
	t.Run("registerWithMiddlewares prevents double registration", func(t *testing.T) {
		router := NewV2()
		controller := NewTestUserController()

		rr := router.Resource("/users", controller).(*resourceWrapperV2)

		// First registration
		rr.registerWithMiddlewares(nil)

		// Second call should be no-op (already registered)
		rr.registerWithMiddlewares(nil)

		// Verify routes still work
		req := httptest.NewRequest("GET", "/users", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
	})
}

func TestResource_WithMiddleware(t *testing.T) {
	t.Run("resource routes receive global middleware", func(t *testing.T) {
		router := NewV2()
		var middlewareCalled bool

		router.Use(func(next HandlerFunc) HandlerFunc {
			return func(c *Context) error {
				middlewareCalled = true
				return next(c)
			}
		})

		controller := NewTestUserController()
		router.Resource("/users", controller)

		req := httptest.NewRequest("GET", "/users", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if !middlewareCalled {
			t.Error("middleware should be called for resource routes")
		}
	})
}

func TestIsValidHandlerSignature_OutputType(t *testing.T) {
	t.Run("rejects method returning non-error type", func(t *testing.T) {
		controller := &NonErrorController{}
		controllerType := reflect.TypeOf(controller)

		method, _ := controllerType.MethodByName("Index")
		if isValidHandlerSignature(method.Type) {
			t.Error("should reject method returning string instead of error")
		}
	})
}

type NonErrorController struct{}

func (c *NonErrorController) Index(ctx *Context) string {
	return "not an error"
}

func TestResource_ControllerWithWrongSignatureMethod(t *testing.T) {
	t.Run("skips resource method with invalid signature", func(t *testing.T) {
		router := NewV2()
		controller := &WrongSignatureResourceController{}

		router.Resource("/items", controller)

		// Index has wrong signature, should be skipped
		// Show has correct signature, should work
		req := httptest.NewRequest("GET", "/items", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// Index should not work (wrong signature)
		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404 for Index with wrong signature, got %d", w.Code)
		}

		// Show should work
		req = httptest.NewRequest("GET", "/items/123", nil)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200 for Show, got %d", w.Code)
		}
	})
}

// WrongSignatureResourceController has Index with wrong signature but Show with correct signature
type WrongSignatureResourceController struct{}

func (c *WrongSignatureResourceController) Index() error { // Wrong: missing *Context param
	return nil
}

func (c *WrongSignatureResourceController) Show(ctx *Context) error { // Correct signature
	return ctx.String(http.StatusOK, "show")
}
