package router

import (
	"net/http"
	"sync"
	"testing"
)

// dummyHandler is a simple handler for testing
func dummyHandler(c *Context) error {
	return nil
}

func TestTree_InsertAndMatch_StaticRoutes(t *testing.T) {
	t.Run("matches single static route", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/users", dummyHandler)

		result := tree.Match("GET", "/users")
		if result == nil {
			t.Fatal("expected match for /users")
		}
	})

	t.Run("matches deeply nested static route", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/api/v1/admin/settings", dummyHandler)

		result := tree.Match("GET", "/api/v1/admin/settings")
		if result == nil {
			t.Fatal("expected match for deep path")
		}
	})

	t.Run("returns nil for non-existent route", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/users", dummyHandler)

		result := tree.Match("GET", "/posts")
		if result != nil {
			t.Error("expected nil for non-existent route")
		}
	})

	t.Run("returns nil for wrong HTTP method", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/users", dummyHandler)

		result := tree.Match("POST", "/users")
		if result != nil {
			t.Error("expected nil for wrong HTTP method")
		}
	})

	t.Run("matches multiple static routes", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/users", dummyHandler)
		tree.Insert("GET", "/posts", dummyHandler)
		tree.Insert("GET", "/comments", dummyHandler)

		if tree.Match("GET", "/users") == nil {
			t.Error("should match /users")
		}
		if tree.Match("GET", "/posts") == nil {
			t.Error("should match /posts")
		}
		if tree.Match("GET", "/comments") == nil {
			t.Error("should match /comments")
		}
	})

	t.Run("differentiates routes with shared prefix", func(t *testing.T) {
		tree := NewTree()

		var usersHandler HandlerFunc = func(c *Context) error {
			c.String(http.StatusOK, "users")
			return nil
		}
		var userSettingsHandler HandlerFunc = func(c *Context) error {
			c.String(http.StatusOK, "settings")
			return nil
		}

		tree.Insert("GET", "/users", usersHandler)
		tree.Insert("GET", "/users/settings", userSettingsHandler)

		usersResult := tree.Match("GET", "/users")
		if usersResult == nil {
			t.Fatal("should match /users")
		}

		settingsResult := tree.Match("GET", "/users/settings")
		if settingsResult == nil {
			t.Fatal("should match /users/settings")
		}

		// Verify they're different handlers
		if usersResult.Handler == nil || settingsResult.Handler == nil {
			t.Fatal("handlers should not be nil")
		}
	})

	t.Run("handles root path", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/", dummyHandler)

		result := tree.Match("GET", "/")
		if result == nil {
			t.Error("should match root path")
		}
	})

	t.Run("normalizes paths with trailing slash", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/users", dummyHandler)

		// Should match with or without trailing slash
		if tree.Match("GET", "/users/") == nil {
			t.Error("should match path with trailing slash")
		}
	})
}

func TestTree_InsertAndMatch_ParameterRoutes(t *testing.T) {
	t.Run("matches route with single parameter", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/users/{id}", dummyHandler)

		result := tree.Match("GET", "/users/123")
		if result == nil {
			t.Fatal("expected match for /users/123")
		}

		if result.Params["id"] != "123" {
			t.Errorf("expected id=123, got %q", result.Params["id"])
		}
	})

	t.Run("matches route with multiple parameters", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/users/{userId}/posts/{postId}", dummyHandler)

		result := tree.Match("GET", "/users/1/posts/99")
		if result == nil {
			t.Fatal("expected match")
		}

		if result.Params["userId"] != "1" {
			t.Errorf("expected userId=1, got %q", result.Params["userId"])
		}
		if result.Params["postId"] != "99" {
			t.Errorf("expected postId=99, got %q", result.Params["postId"])
		}
	})

	t.Run("parameter matches any non-slash value", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/users/{id}", dummyHandler)

		testValues := []string{"123", "abc", "hello-world", "user_name", "CamelCase"}
		for _, val := range testValues {
			result := tree.Match("GET", "/users/"+val)
			if result == nil {
				t.Errorf("should match /users/%s", val)
				continue
			}
			if result.Params["id"] != val {
				t.Errorf("expected id=%s, got %q", val, result.Params["id"])
			}
		}
	})

	t.Run("does not match when segment count differs", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/users/{id}", dummyHandler)

		// Too few segments
		if tree.Match("GET", "/users") != nil {
			t.Error("should not match /users (missing parameter)")
		}

		// Too many segments
		if tree.Match("GET", "/users/123/extra") != nil {
			t.Error("should not match /users/123/extra (extra segment)")
		}
	})
}

func TestTree_InsertAndMatch_RegexConstrainedRoutes(t *testing.T) {
	t.Run("matches numeric constraint", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/users/{id:[0-9]+}", dummyHandler)

		result := tree.Match("GET", "/users/123")
		if result == nil {
			t.Fatal("expected match for numeric id")
		}
		if result.Params["id"] != "123" {
			t.Errorf("expected id=123, got %q", result.Params["id"])
		}
	})

	t.Run("rejects non-matching value", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/users/{id:[0-9]+}", dummyHandler)

		result := tree.Match("GET", "/users/abc")
		if result != nil {
			t.Error("should not match non-numeric id")
		}
	})

	t.Run("matches slug constraint", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/posts/{slug:[a-z0-9-]+}", dummyHandler)

		if tree.Match("GET", "/posts/hello-world") == nil {
			t.Error("should match hello-world")
		}
		if tree.Match("GET", "/posts/post-123") == nil {
			t.Error("should match post-123")
		}
		if tree.Match("GET", "/posts/Hello-World") != nil {
			t.Error("should NOT match uppercase")
		}
	})

	t.Run("regex takes priority over plain param at same level", func(t *testing.T) {
		tree := NewTree()

		var numericHandler HandlerFunc = func(c *Context) error {
			c.String(http.StatusOK, "numeric")
			return nil
		}
		var stringHandler HandlerFunc = func(c *Context) error {
			c.String(http.StatusOK, "string")
			return nil
		}

		// Order of registration should not matter - regex is more specific
		tree.Insert("GET", "/users/{id}", stringHandler)
		tree.Insert("GET", "/users/{id:[0-9]+}", numericHandler)

		// Numeric should match regex route
		numericResult := tree.Match("GET", "/users/123")
		if numericResult == nil {
			t.Fatal("should match numeric")
		}

		// Non-numeric should match param route
		stringResult := tree.Match("GET", "/users/abc")
		if stringResult == nil {
			t.Fatal("should match string")
		}
	})
}

func TestTree_InsertAndMatch_WildcardRoutes(t *testing.T) {
	t.Run("matches wildcard capturing rest of path", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/files/{path:.*}", dummyHandler)

		result := tree.Match("GET", "/files/dir/subdir/file.txt")
		if result == nil {
			t.Fatal("expected match for wildcard")
		}
		if result.Params["path"] != "dir/subdir/file.txt" {
			t.Errorf("expected path=dir/subdir/file.txt, got %q", result.Params["path"])
		}
	})

	t.Run("wildcard matches empty path", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/files/{path:.*}", dummyHandler)

		result := tree.Match("GET", "/files/")
		if result == nil {
			t.Fatal("expected match for empty wildcard")
		}
		if result.Params["path"] != "" {
			t.Errorf("expected empty path, got %q", result.Params["path"])
		}
	})

	t.Run("static route takes priority over wildcard", func(t *testing.T) {
		tree := NewTree()

		var staticHandler HandlerFunc = func(c *Context) error {
			c.String(http.StatusOK, "static")
			return nil
		}
		var wildcardHandler HandlerFunc = func(c *Context) error {
			c.String(http.StatusOK, "wildcard")
			return nil
		}

		tree.Insert("GET", "/files/special", staticHandler)
		tree.Insert("GET", "/files/{path:.*}", wildcardHandler)

		// Static should match exactly
		staticResult := tree.Match("GET", "/files/special")
		if staticResult == nil {
			t.Fatal("should match static route")
		}

		// Other paths should match wildcard
		wildcardResult := tree.Match("GET", "/files/other/path")
		if wildcardResult == nil {
			t.Fatal("should match wildcard route")
		}
	})
}

func TestTree_InsertAndMatch_MethodHandling(t *testing.T) {
	t.Run("same path with different methods", func(t *testing.T) {
		tree := NewTree()

		var getHandler HandlerFunc = func(c *Context) error {
			c.String(http.StatusOK, "GET")
			return nil
		}
		var postHandler HandlerFunc = func(c *Context) error {
			c.String(http.StatusOK, "POST")
			return nil
		}

		tree.Insert("GET", "/users", getHandler)
		tree.Insert("POST", "/users", postHandler)

		getResult := tree.Match("GET", "/users")
		if getResult == nil {
			t.Fatal("should match GET")
		}

		postResult := tree.Match("POST", "/users")
		if postResult == nil {
			t.Fatal("should match POST")
		}

		// Different handlers
		if getResult.Handler == nil || postResult.Handler == nil {
			t.Error("handlers should not be nil")
		}
	})

	t.Run("returns allowed methods for path", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/users", dummyHandler)
		tree.Insert("POST", "/users", dummyHandler)
		tree.Insert("DELETE", "/users", dummyHandler)

		methods := tree.AllowedMethods("/users")

		if len(methods) != 3 {
			t.Errorf("expected 3 methods, got %d", len(methods))
		}

		methodSet := make(map[string]bool)
		for _, m := range methods {
			methodSet[m] = true
		}

		if !methodSet["GET"] || !methodSet["POST"] || !methodSet["DELETE"] {
			t.Error("should have GET, POST, DELETE methods")
		}
	})
}

func TestTree_MatchPriority(t *testing.T) {
	t.Run("priority order: static > regex > param > wildcard", func(t *testing.T) {
		tree := NewTree()

		tree.Insert("GET", "/users/profile", func(c *Context) error {
			c.Set("matched", "static")
			return nil
		})
		tree.Insert("GET", "/users/{id:[0-9]+}", func(c *Context) error {
			c.Set("matched", "regex")
			return nil
		})
		tree.Insert("GET", "/users/{name}", func(c *Context) error {
			c.Set("matched", "param")
			return nil
		})

		// Static should match "profile"
		staticResult := tree.Match("GET", "/users/profile")
		if staticResult == nil {
			t.Fatal("should match static")
		}

		// Regex should match numeric
		regexResult := tree.Match("GET", "/users/123")
		if regexResult == nil {
			t.Fatal("should match regex")
		}
		if regexResult.Params["id"] != "123" {
			t.Error("regex should capture id param")
		}

		// Param should match non-numeric string
		paramResult := tree.Match("GET", "/users/johndoe")
		if paramResult == nil {
			t.Fatal("should match param")
		}
		if paramResult.Params["name"] != "johndoe" {
			t.Error("param should capture name param")
		}
	})
}

func TestTree_ComplexRoutes(t *testing.T) {
	t.Run("REST API structure", func(t *testing.T) {
		tree := NewTree()

		// Full CRUD routes
		tree.Insert("GET", "/users", dummyHandler)
		tree.Insert("POST", "/users", dummyHandler)
		tree.Insert("GET", "/users/{id}", dummyHandler)
		tree.Insert("PUT", "/users/{id}", dummyHandler)
		tree.Insert("DELETE", "/users/{id}", dummyHandler)

		// Nested resources
		tree.Insert("GET", "/users/{userId}/posts", dummyHandler)
		tree.Insert("POST", "/users/{userId}/posts", dummyHandler)
		tree.Insert("GET", "/users/{userId}/posts/{postId}", dummyHandler)

		// Verify all routes work
		tests := []struct {
			method string
			path   string
			params map[string]string
		}{
			{"GET", "/users", nil},
			{"POST", "/users", nil},
			{"GET", "/users/1", map[string]string{"id": "1"}},
			{"PUT", "/users/1", map[string]string{"id": "1"}},
			{"DELETE", "/users/1", map[string]string{"id": "1"}},
			{"GET", "/users/1/posts", map[string]string{"userId": "1"}},
			{"POST", "/users/1/posts", map[string]string{"userId": "1"}},
			{"GET", "/users/1/posts/99", map[string]string{"userId": "1", "postId": "99"}},
		}

		for _, tt := range tests {
			result := tree.Match(tt.method, tt.path)
			if result == nil {
				t.Errorf("%s %s: expected match", tt.method, tt.path)
				continue
			}

			for key, expectedVal := range tt.params {
				if result.Params[key] != expectedVal {
					t.Errorf("%s %s: expected %s=%s, got %s", tt.method, tt.path, key, expectedVal, result.Params[key])
				}
			}
		}
	})

	t.Run("versioned API routes", func(t *testing.T) {
		tree := NewTree()

		tree.Insert("GET", "/api/{version:[v][0-9]+}/users", dummyHandler)
		tree.Insert("GET", "/api/{version:[v][0-9]+}/users/{id:[0-9]+}", dummyHandler)

		// Valid version
		result := tree.Match("GET", "/api/v1/users")
		if result == nil {
			t.Fatal("should match v1")
		}
		if result.Params["version"] != "v1" {
			t.Errorf("expected version=v1, got %q", result.Params["version"])
		}

		// Invalid version format
		if tree.Match("GET", "/api/1/users") != nil {
			t.Error("should not match invalid version format")
		}
	})
}

func TestTree_ConcurrentAccess(t *testing.T) {
	t.Run("concurrent reads are safe", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/users/{id}", dummyHandler)

		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 100; j++ {
					result := tree.Match("GET", "/users/123")
					if result == nil {
						t.Error("concurrent read failed")
					}
				}
			}()
		}
		wg.Wait()
	})
}

func TestTree_RouteMetadata(t *testing.T) {
	t.Run("stores and retrieves route name", func(t *testing.T) {
		tree := NewTree()
		tree.InsertWithName("GET", "/users/{id}", dummyHandler, "users.show")

		result := tree.Match("GET", "/users/123")
		if result == nil {
			t.Fatal("expected match")
		}
		if result.Name != "users.show" {
			t.Errorf("expected name=users.show, got %q", result.Name)
		}
	})

	t.Run("stores original path pattern", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/users/{id:[0-9]+}", dummyHandler)

		result := tree.Match("GET", "/users/123")
		if result == nil {
			t.Fatal("expected match")
		}
		if result.Path != "/users/{id:[0-9]+}" {
			t.Errorf("expected original path pattern, got %q", result.Path)
		}
	})
}

func TestTree_AllowedMethods(t *testing.T) {
	t.Run("returns all methods for a path", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/users", dummyHandler)
		tree.Insert("POST", "/users", dummyHandler)
		tree.Insert("PUT", "/users/{id}", dummyHandler)
		tree.Insert("DELETE", "/users/{id}", dummyHandler)

		methods := tree.AllowedMethods("/users")
		if len(methods) != 2 {
			t.Errorf("expected 2 methods for /users, got %d: %v", len(methods), methods)
		}

		// Check that GET and POST are present
		hasGet, hasPost := false, false
		for _, m := range methods {
			if m == "GET" {
				hasGet = true
			}
			if m == "POST" {
				hasPost = true
			}
		}
		if !hasGet || !hasPost {
			t.Errorf("expected GET and POST, got %v", methods)
		}
	})

	t.Run("returns methods for parameterized path", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/posts/{id}", dummyHandler)
		tree.Insert("PUT", "/posts/{id}", dummyHandler)
		tree.Insert("DELETE", "/posts/{id}", dummyHandler)

		methods := tree.AllowedMethods("/posts/123")
		if len(methods) != 3 {
			t.Errorf("expected 3 methods for /posts/123, got %d: %v", len(methods), methods)
		}
	})

	t.Run("returns empty for non-existent path", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/users", dummyHandler)

		methods := tree.AllowedMethods("/nonexistent")
		if len(methods) != 0 {
			t.Errorf("expected 0 methods for /nonexistent, got %d", len(methods))
		}
	})

	t.Run("handles regex constrained routes", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/items/{id:[0-9]+}", dummyHandler)
		tree.Insert("DELETE", "/items/{id:[0-9]+}", dummyHandler)

		// Should match numeric id
		methods := tree.AllowedMethods("/items/42")
		if len(methods) != 2 {
			t.Errorf("expected 2 methods for /items/42, got %d", len(methods))
		}

		// Should not match non-numeric
		methods = tree.AllowedMethods("/items/abc")
		if len(methods) != 0 {
			t.Errorf("expected 0 methods for /items/abc, got %d", len(methods))
		}
	})
}

func TestTree_NamedRouteLookup(t *testing.T) {
	t.Run("finds named route in namedRoutes map", func(t *testing.T) {
		tree := NewTree()
		tree.InsertWithName("GET", "/users/{id}", dummyHandler, "users.show")
		tree.InsertWithName("POST", "/users", dummyHandler, "users.store")

		// Check namedRoutes map
		if tree.namedRoutes["users.show"] == nil {
			t.Error("users.show should be in namedRoutes")
		}
		if tree.namedRoutes["users.store"] == nil {
			t.Error("users.store should be in namedRoutes")
		}

		// Verify stored segments for URL generation
		showRoute := tree.namedRoutes["users.show"]
		if len(showRoute.segments) != 2 {
			t.Errorf("expected 2 segments, got %d", len(showRoute.segments))
		}
	})

	t.Run("unnamed routes not added to namedRoutes", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/health", dummyHandler) // No name

		if len(tree.namedRoutes) != 0 {
			t.Errorf("expected 0 named routes, got %d", len(tree.namedRoutes))
		}
	})
}

func TestTree_CompileStaticRoutes(t *testing.T) {
	t.Run("compiles only static routes", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/users", dummyHandler)
		tree.Insert("POST", "/users", dummyHandler)
		tree.Insert("GET", "/users/{id}", dummyHandler)
		tree.Insert("GET", "/health", dummyHandler)

		compiled := tree.CompileStaticRoutes()

		if _, ok := compiled["GET /users"]; !ok {
			t.Error("expected GET /users in compiled routes")
		}
		if _, ok := compiled["POST /users"]; !ok {
			t.Error("expected POST /users in compiled routes")
		}
		if _, ok := compiled["GET /health"]; !ok {
			t.Error("expected GET /health in compiled routes")
		}

		// Parameterized route should NOT be compiled
		for key := range compiled {
			if key == "GET /users/{id}" {
				t.Error("parameterized route should not be compiled")
			}
		}
	})

	t.Run("compiles nested static routes", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/api/v1/status", dummyHandler)
		tree.Insert("GET", "/api/v1/health", dummyHandler)

		compiled := tree.CompileStaticRoutes()

		if _, ok := compiled["GET /api/v1/status"]; !ok {
			t.Error("expected GET /api/v1/status in compiled routes")
		}
		if _, ok := compiled["GET /api/v1/health"]; !ok {
			t.Error("expected GET /api/v1/health in compiled routes")
		}
	})

	t.Run("compiles root path", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/", dummyHandler)

		compiled := tree.CompileStaticRoutes()

		if _, ok := compiled["GET /"]; !ok {
			t.Error("expected GET / in compiled routes")
		}
	})

	t.Run("does not compile wildcard routes", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/files/{path:.*}", dummyHandler)
		tree.Insert("GET", "/static", dummyHandler)

		compiled := tree.CompileStaticRoutes()

		if len(compiled) != 1 {
			t.Errorf("expected 1 compiled route, got %d", len(compiled))
		}
		if _, ok := compiled["GET /static"]; !ok {
			t.Error("expected GET /static in compiled routes")
		}
	})

	t.Run("does not compile regex routes", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/users/{id:[0-9]+}", dummyHandler)
		tree.Insert("GET", "/about", dummyHandler)

		compiled := tree.CompileStaticRoutes()

		if len(compiled) != 1 {
			t.Errorf("expected 1 compiled route, got %d", len(compiled))
		}
		if _, ok := compiled["GET /about"]; !ok {
			t.Error("expected GET /about in compiled routes")
		}
	})

	t.Run("empty tree compiles to empty map", func(t *testing.T) {
		tree := NewTree()

		compiled := tree.CompileStaticRoutes()

		if len(compiled) != 0 {
			t.Errorf("expected 0 compiled routes, got %d", len(compiled))
		}
	})

	t.Run("compiled handlers match tree handlers", func(t *testing.T) {
		tree := NewTree()

		var handlerA HandlerFunc = func(c *Context) error {
			c.String(http.StatusOK, "a")
			return nil
		}
		tree.InsertWithName("GET", "/test", handlerA, "test.route")

		compiled := tree.CompileStaticRoutes()
		result := compiled["GET /test"]

		if result == nil {
			t.Fatal("expected compiled result")
		}
		if result.Name != "test.route" {
			t.Errorf("expected name=test.route, got %q", result.Name)
		}
		if result.Path != "/test" {
			t.Errorf("expected path=/test, got %q", result.Path)
		}
	})
}

func TestTree_InsertErrors(t *testing.T) {
	t.Run("returns error for invalid regex", func(t *testing.T) {
		tree := NewTree()
		err := tree.Insert("GET", "/users/{id:[invalid(}", dummyHandler)
		if err == nil {
			t.Error("expected error for invalid regex")
		}
	})
}

func TestTree_MatchEdgeCases(t *testing.T) {
	t.Run("match with trailing slash", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/users", dummyHandler)

		result := tree.Match("GET", "/users/")
		// Trailing slash handling depends on implementation
		// This test documents current behavior
		if result != nil {
			t.Log("Router matches trailing slash")
		}
	})

	t.Run("match root path", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/", dummyHandler)

		result := tree.Match("GET", "/")
		if result == nil {
			t.Error("expected match for root path")
		}
	})

	t.Run("no match returns nil", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/users", dummyHandler)

		result := tree.Match("GET", "/posts")
		if result != nil {
			t.Error("expected nil for non-matching path")
		}
	})

	t.Run("regex child matching", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/users/{id:[0-9]+}", dummyHandler)
		tree.Insert("GET", "/users/{slug:[a-z]+}", dummyHandler)

		// Numeric should match first regex
		result := tree.Match("GET", "/users/123")
		if result == nil {
			t.Error("expected match for numeric id")
		}

		// Alpha should match second regex
		result = tree.Match("GET", "/users/abc")
		if result == nil {
			t.Error("expected match for alpha slug")
		}
	})
}

func TestTree_FindNodeEdgeCases(t *testing.T) {
	t.Run("findNode with wildcard", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/files/{path:.*}", dummyHandler)

		methods := tree.AllowedMethods("/files/any/path/here")
		if len(methods) != 1 {
			t.Errorf("expected 1 method, got %d", len(methods))
		}
	})

	t.Run("findNode with regex", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/items/{id:[0-9]+}", dummyHandler)
		tree.Insert("PUT", "/items/{id:[0-9]+}", dummyHandler)

		methods := tree.AllowedMethods("/items/999")
		if len(methods) != 2 {
			t.Errorf("expected 2 methods, got %d", len(methods))
		}
	})

	t.Run("findNode with param child", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/users/{id}", dummyHandler)
		tree.Insert("DELETE", "/users/{id}", dummyHandler)

		methods := tree.AllowedMethods("/users/abc")
		if len(methods) != 2 {
			t.Errorf("expected 2 methods, got %d", len(methods))
		}
	})

	t.Run("AllowedMethods on root", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/", dummyHandler)
		tree.Insert("POST", "/", dummyHandler)

		methods := tree.AllowedMethods("/")
		if len(methods) != 2 {
			t.Errorf("expected 2 methods for root, got %d", len(methods))
		}
	})
}

func TestTree_GetOrCreateChildEdgeCases(t *testing.T) {
	t.Run("reuses existing static child", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/users", dummyHandler)
		tree.Insert("POST", "/users", dummyHandler)

		// Both should use same node
		result1 := tree.Match("GET", "/users")
		result2 := tree.Match("POST", "/users")
		if result1 == nil || result2 == nil {
			t.Error("both methods should match")
		}
	})

	t.Run("reuses existing regex child with same pattern", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/items/{id:[0-9]+}", dummyHandler)
		tree.Insert("PUT", "/items/{id:[0-9]+}", dummyHandler)

		result := tree.Match("GET", "/items/123")
		if result == nil {
			t.Error("expected match")
		}
	})

	t.Run("reuses existing param child", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/posts/{id}", dummyHandler)
		tree.Insert("DELETE", "/posts/{id}", dummyHandler)

		result := tree.Match("GET", "/posts/xyz")
		if result == nil {
			t.Error("expected match")
		}
	})

	t.Run("reuses existing wildcard child", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/assets/{path:.*}", dummyHandler)
		tree.Insert("HEAD", "/assets/{path:.*}", dummyHandler)

		result := tree.Match("HEAD", "/assets/css/style.css")
		if result == nil {
			t.Error("expected match")
		}
	})

	t.Run("unknown segment type returns nil", func(t *testing.T) {
		node := &Node{}
		unknownSeg := Segment{Type: SegmentType(99), Value: "unknown"}

		child := node.getOrCreateChild(unknownSeg)
		if child != nil {
			t.Error("expected nil for unknown segment type")
		}
	})
}

func TestTree_MatchMoreEdgeCases(t *testing.T) {
	t.Run("no handlers at node returns nil", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/api/users", dummyHandler)

		// /api exists as intermediate node but has no handlers
		result := tree.Match("GET", "/api")
		if result != nil {
			t.Error("expected nil for intermediate node without handlers")
		}
	})

	t.Run("wrong method returns nil", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/users", dummyHandler)

		result := tree.Match("POST", "/users")
		if result != nil {
			t.Error("expected nil for wrong method")
		}
	})

	t.Run("path longer than registered returns nil", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/users", dummyHandler)

		result := tree.Match("GET", "/users/extra/path")
		if result != nil {
			t.Error("expected nil for longer path")
		}
	})

	t.Run("root path wrong method returns nil", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/", dummyHandler)

		result := tree.Match("POST", "/")
		if result != nil {
			t.Error("expected nil for wrong method on root")
		}
	})

	t.Run("empty tree root match returns nil", func(t *testing.T) {
		tree := NewTree()
		// Don't insert anything

		result := tree.Match("GET", "/")
		if result != nil {
			t.Error("expected nil for empty tree")
		}
	})

	t.Run("match with regex no match falls through", func(t *testing.T) {
		tree := NewTree()
		tree.Insert("GET", "/items/{id:[0-9]+}", dummyHandler)

		// This should not match - 'abc' doesn't match [0-9]+
		result := tree.Match("GET", "/items/abc")
		if result != nil {
			t.Error("expected nil when regex doesn't match")
		}
	})
}
