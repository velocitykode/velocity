package router

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSetParams_GetParams(t *testing.T) {
	t.Run("stores and retrieves params", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/users/123", nil)

		params := map[string]string{"id": "123", "name": "test"}
		req = SetParams(req, params)

		retrieved := GetParams(req)

		if retrieved["id"] != "123" {
			t.Errorf("expected id=123, got %q", retrieved["id"])
		}
		if retrieved["name"] != "test" {
			t.Errorf("expected name=test, got %q", retrieved["name"])
		}
	})

	t.Run("returns empty map when no params set", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/users", nil)

		retrieved := GetParams(req)

		if retrieved == nil {
			t.Error("expected empty map, got nil")
		}
		if len(retrieved) != 0 {
			t.Errorf("expected empty map, got %d entries", len(retrieved))
		}
	})

	t.Run("does not modify original request", func(t *testing.T) {
		original := httptest.NewRequest("GET", "/users", nil)

		params := map[string]string{"id": "123"}
		modified := SetParams(original, params)

		// Original should not have params
		origParams := GetParams(original)
		if len(origParams) != 0 {
			t.Error("original request should not have params")
		}

		// Modified should have params
		modParams := GetParams(modified)
		if modParams["id"] != "123" {
			t.Error("modified request should have params")
		}
	})

	t.Run("multiple params", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/users/1/posts/99", nil)

		params := map[string]string{
			"userId": "1",
			"postId": "99",
		}
		req = SetParams(req, params)

		retrieved := GetParams(req)

		if retrieved["userId"] != "1" {
			t.Errorf("expected userId=1, got %q", retrieved["userId"])
		}
		if retrieved["postId"] != "99" {
			t.Errorf("expected postId=99, got %q", retrieved["postId"])
		}
	})
}

func TestSetRouteName_GetRouteName(t *testing.T) {
	t.Run("stores and retrieves route name", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/users/123", nil)

		req = SetRouteName(req, "users.show")

		name := GetRouteName(req)
		if name != "users.show" {
			t.Errorf("expected users.show, got %q", name)
		}
	})

	t.Run("returns empty string when no name set", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/users", nil)

		name := GetRouteName(req)
		if name != "" {
			t.Errorf("expected empty string, got %q", name)
		}
	})

	t.Run("can set both params and route name", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/users/123", nil)

		req = SetParams(req, map[string]string{"id": "123"})
		req = SetRouteName(req, "users.show")

		// Both should be retrievable
		params := GetParams(req)
		if params["id"] != "123" {
			t.Error("params should be preserved")
		}

		name := GetRouteName(req)
		if name != "users.show" {
			t.Error("route name should be preserved")
		}
	})
}

func TestParams_Integration(t *testing.T) {
	t.Run("works with handler chain", func(t *testing.T) {
		var capturedParams map[string]string
		var capturedName string

		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedParams = GetParams(r)
			capturedName = GetRouteName(r)
		})

		// Simulate middleware setting params
		middleware := func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r = SetParams(r, map[string]string{"id": "42"})
				r = SetRouteName(r, "test.route")
				next.ServeHTTP(w, r)
			})
		}

		req := httptest.NewRequest("GET", "/test/42", nil)
		w := httptest.NewRecorder()

		middleware(handler).ServeHTTP(w, req)

		if capturedParams["id"] != "42" {
			t.Errorf("expected id=42, got %q", capturedParams["id"])
		}
		if capturedName != "test.route" {
			t.Errorf("expected test.route, got %q", capturedName)
		}
	})
}
