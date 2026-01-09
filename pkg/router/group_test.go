package router

import (
	"net/http"
	"testing"
)

func TestGroupDefinition_BasicUsage(t *testing.T) {
	t.Run("creates group with prefix", func(t *testing.T) {
		group := NewGroupDefinition("/api", nil)

		if group.prefix != "/api" {
			t.Errorf("expected prefix /api, got %q", group.prefix)
		}
	})

	t.Run("adds routes to group", func(t *testing.T) {
		group := NewGroupDefinition("/api", nil)

		handler := func(c *Context) error { return nil }
		group.AddRoute("GET", "/users", handler)
		group.AddRoute("POST", "/users", handler)

		if len(group.routes) != 2 {
			t.Errorf("expected 2 routes, got %d", len(group.routes))
		}
	})

	t.Run("creates child groups", func(t *testing.T) {
		root := NewGroupDefinition("", nil)
		api := root.AddChild("/api")
		v1 := api.AddChild("/v1")

		if len(root.children) != 1 {
			t.Errorf("expected 1 child, got %d", len(root.children))
		}

		if v1.FullPrefix() != "/api/v1" {
			t.Errorf("expected full prefix /api/v1, got %q", v1.FullPrefix())
		}
	})
}

func TestGroupDefinition_MiddlewareInheritance(t *testing.T) {
	t.Run("child inherits parent middleware at creation time", func(t *testing.T) {
		var executionOrder []string

		parentMw := func(next HandlerFunc) HandlerFunc {
			return func(c *Context) error {
				executionOrder = append(executionOrder, "parent")
				return next(c)
			}
		}

		parent := NewGroupDefinition("/api", nil)
		parent.Use(parentMw)

		// Child created AFTER parent middleware
		child := parent.AddChild("/v1")

		handler := func(c *Context) error {
			executionOrder = append(executionOrder, "handler")
			return nil
		}
		child.AddRoute("GET", "/users", handler)

		tree := NewTree()
		parent.CommitToTree(tree, nil)

		result := tree.Match("GET", "/api/v1/users")
		ctx := &Context{}
		result.Handler(ctx)

		// Parent middleware should execute
		expected := []string{"parent", "handler"}
		if len(executionOrder) != len(expected) {
			t.Fatalf("expected %d executions, got %d: %v", len(expected), len(executionOrder), executionOrder)
		}
	})

	t.Run("child can add own middleware", func(t *testing.T) {
		var executionOrder []string

		parentMw := func(next HandlerFunc) HandlerFunc {
			return func(c *Context) error {
				executionOrder = append(executionOrder, "parent")
				return next(c)
			}
		}
		childMw := func(next HandlerFunc) HandlerFunc {
			return func(c *Context) error {
				executionOrder = append(executionOrder, "child")
				return next(c)
			}
		}

		parent := NewGroupDefinition("/api", nil)
		parent.Use(parentMw)

		child := parent.AddChild("/v1")
		child.Use(childMw)

		handler := func(c *Context) error {
			executionOrder = append(executionOrder, "handler")
			return nil
		}
		child.AddRoute("GET", "/users", handler)

		tree := NewTree()
		parent.CommitToTree(tree, nil)

		result := tree.Match("GET", "/api/v1/users")
		ctx := &Context{}
		result.Handler(ctx)

		// Both parent and child middleware should execute
		expected := []string{"parent", "child", "handler"}
		if len(executionOrder) != len(expected) {
			t.Fatalf("expected %d executions, got %d: %v", len(expected), len(executionOrder), executionOrder)
		}
	})
}

func TestGroupDefinition_DeferredMiddleware(t *testing.T) {
	t.Run("middleware applied after route definition affects routes", func(t *testing.T) {
		var executionOrder []string

		mw := func(next HandlerFunc) HandlerFunc {
			return func(c *Context) error {
				executionOrder = append(executionOrder, "middleware")
				return next(c)
			}
		}

		handler := func(c *Context) error {
			executionOrder = append(executionOrder, "handler")
			return nil
		}

		group := NewGroupDefinition("/api", nil)

		// Add route FIRST
		group.AddRoute("GET", "/users", handler)

		// Add middleware AFTER
		group.Use(mw)

		// Verify middleware is applied when committed
		tree := NewTree()
		group.CommitToTree(tree, nil)

		result := tree.Match("GET", "/api/users")
		if result == nil {
			t.Fatal("expected match")
		}

		ctx := &Context{}
		result.Handler(ctx)

		// Middleware should have executed
		expected := []string{"middleware", "handler"}
		if len(executionOrder) != len(expected) {
			t.Fatalf("expected %d executions, got %d: %v", len(expected), len(executionOrder), executionOrder)
		}
		for i, exp := range expected {
			if executionOrder[i] != exp {
				t.Errorf("execution[%d]: expected %q, got %q", i, exp, executionOrder[i])
			}
		}
	})

	t.Run("multiple middleware applied in order", func(t *testing.T) {
		var executionOrder []string

		mw1 := func(next HandlerFunc) HandlerFunc {
			return func(c *Context) error {
				executionOrder = append(executionOrder, "mw1")
				return next(c)
			}
		}
		mw2 := func(next HandlerFunc) HandlerFunc {
			return func(c *Context) error {
				executionOrder = append(executionOrder, "mw2")
				return next(c)
			}
		}

		group := NewGroupDefinition("/api", nil)
		handler := func(c *Context) error {
			executionOrder = append(executionOrder, "handler")
			return nil
		}

		group.AddRoute("GET", "/users", handler)
		group.Use(mw1, mw2)

		tree := NewTree()
		group.CommitToTree(tree, nil)

		result := tree.Match("GET", "/api/users")
		ctx := &Context{}
		result.Handler(ctx)

		expected := []string{"mw1", "mw2", "handler"}
		if len(executionOrder) != len(expected) {
			t.Fatalf("expected %d executions, got %d: %v", len(expected), len(executionOrder), executionOrder)
		}
	})

	t.Run("deferred middleware propagates to child groups", func(t *testing.T) {
		var executionOrder []string

		mw := func(next HandlerFunc) HandlerFunc {
			return func(c *Context) error {
				executionOrder = append(executionOrder, "middleware")
				return next(c)
			}
		}

		parent := NewGroupDefinition("/api", nil)
		child := parent.AddChild("/v1")

		handler := func(c *Context) error {
			executionOrder = append(executionOrder, "handler")
			return nil
		}
		child.AddRoute("GET", "/users", handler)

		// Add middleware to parent AFTER child route is defined
		parent.Use(mw)

		tree := NewTree()
		parent.CommitToTree(tree, nil)

		result := tree.Match("GET", "/api/v1/users")
		if result == nil {
			t.Fatal("expected match")
		}

		ctx := &Context{}
		result.Handler(ctx)

		expected := []string{"middleware", "handler"}
		if len(executionOrder) != len(expected) {
			t.Fatalf("expected %d executions, got %d: %v", len(expected), len(executionOrder), executionOrder)
		}
	})
}

func TestGroupDefinition_CommitToTree(t *testing.T) {
	t.Run("commits routes with correct paths", func(t *testing.T) {
		tree := NewTree()
		group := NewGroupDefinition("/api", nil)

		handler := func(c *Context) error {
			c.String(http.StatusOK, "ok")
			return nil
		}

		group.AddRoute("GET", "/users", handler)
		group.AddRoute("POST", "/users", handler)
		group.AddRoute("GET", "/users/{id}", handler)

		err := group.CommitToTree(tree, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify routes are accessible
		if tree.Match("GET", "/api/users") == nil {
			t.Error("expected GET /api/users to match")
		}
		if tree.Match("POST", "/api/users") == nil {
			t.Error("expected POST /api/users to match")
		}
		if tree.Match("GET", "/api/users/123") == nil {
			t.Error("expected GET /api/users/123 to match")
		}
	})

	t.Run("commits nested groups", func(t *testing.T) {
		tree := NewTree()

		root := NewGroupDefinition("", nil)
		api := root.AddChild("/api")
		v1 := api.AddChild("/v1")

		handler := func(c *Context) error { return nil }
		v1.AddRoute("GET", "/users", handler)

		err := root.CommitToTree(tree, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if tree.Match("GET", "/api/v1/users") == nil {
			t.Error("expected GET /api/v1/users to match")
		}
	})

	t.Run("applies middleware chain correctly", func(t *testing.T) {
		var executionOrder []string

		globalMw := func(next HandlerFunc) HandlerFunc {
			return func(c *Context) error {
				executionOrder = append(executionOrder, "global")
				return next(c)
			}
		}
		groupMw := func(next HandlerFunc) HandlerFunc {
			return func(c *Context) error {
				executionOrder = append(executionOrder, "group")
				return next(c)
			}
		}
		routeMw := func(next HandlerFunc) HandlerFunc {
			return func(c *Context) error {
				executionOrder = append(executionOrder, "route")
				return next(c)
			}
		}

		handler := func(c *Context) error {
			executionOrder = append(executionOrder, "handler")
			return nil
		}

		tree := NewTree()
		group := NewGroupDefinition("/api", nil)
		group.Use(groupMw)

		route := group.AddRoute("GET", "/users", handler)
		route.Middlewares = append(route.Middlewares, routeMw)

		err := group.CommitToTree(tree, []MiddlewareFunc{globalMw})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Execute the route
		result := tree.Match("GET", "/api/users")
		if result == nil {
			t.Fatal("expected match")
		}

		// Create context and call handler
		ctx := &Context{}
		result.Handler(ctx)

		// Verify execution order: global -> group -> route -> handler
		expected := []string{"global", "group", "route", "handler"}
		if len(executionOrder) != len(expected) {
			t.Fatalf("expected %d executions, got %d: %v", len(expected), len(executionOrder), executionOrder)
		}

		for i, exp := range expected {
			if executionOrder[i] != exp {
				t.Errorf("execution[%d]: expected %q, got %q", i, exp, executionOrder[i])
			}
		}
	})

	t.Run("commits route with name", func(t *testing.T) {
		tree := NewTree()
		group := NewGroupDefinition("/api", nil)

		handler := func(c *Context) error { return nil }
		route := group.AddRoute("GET", "/users", handler)
		route.Name = "api.users.index"

		err := group.CommitToTree(tree, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		result := tree.Match("GET", "/api/users")
		if result == nil {
			t.Fatal("expected match")
		}
		if result.Name != "api.users.index" {
			t.Errorf("expected name api.users.index, got %q", result.Name)
		}
	})
}

func TestGroupDefinition_FluentAPI(t *testing.T) {
	t.Run("simulates Group().Use() pattern", func(t *testing.T) {
		var executionOrder []string

		authMw := func(next HandlerFunc) HandlerFunc {
			return func(c *Context) error {
				executionOrder = append(executionOrder, "auth")
				return next(c)
			}
		}

		handler := func(c *Context) error {
			executionOrder = append(executionOrder, "handler")
			return nil
		}

		tree := NewTree()
		root := NewGroupDefinition("", nil)

		// Simulates: r.Group("/api", func(api Router) { api.Get("/users", handler) }).Use(authMw)
		api := root.AddChild("/api")
		api.AddRoute("GET", "/users", handler)

		// Use after routes defined - key feature!
		api.Use(authMw)

		err := root.CommitToTree(tree, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		result := tree.Match("GET", "/api/users")
		if result == nil {
			t.Fatal("expected match")
		}

		ctx := &Context{}
		result.Handler(ctx)

		expected := []string{"auth", "handler"}
		if len(executionOrder) != len(expected) {
			t.Fatalf("expected %d executions, got %d", len(expected), len(executionOrder))
		}

		for i, exp := range expected {
			if executionOrder[i] != exp {
				t.Errorf("execution[%d]: expected %q, got %q", i, exp, executionOrder[i])
			}
		}
	})
}

func TestGroupDefinition_FullPrefixEdgeCases(t *testing.T) {
	t.Run("child with empty prefix returns parent prefix", func(t *testing.T) {
		parent := NewGroupDefinition("/api", nil)
		child := parent.AddChild("") // Empty prefix child

		if child.FullPrefix() != "/api" {
			t.Errorf("expected /api, got %q", child.FullPrefix())
		}
	})

	t.Run("parent with empty prefix returns child prefix", func(t *testing.T) {
		parent := NewGroupDefinition("", nil)
		child := parent.AddChild("/api")

		if child.FullPrefix() != "/api" {
			t.Errorf("expected /api, got %q", child.FullPrefix())
		}
	})

	t.Run("both empty returns empty", func(t *testing.T) {
		parent := NewGroupDefinition("", nil)
		child := parent.AddChild("")

		if child.FullPrefix() != "" {
			t.Errorf("expected empty, got %q", child.FullPrefix())
		}
	})
}

func TestGroupDefinition_CommitToTreeError(t *testing.T) {
	t.Run("returns error for invalid regex pattern", func(t *testing.T) {
		tree := NewTree()
		group := NewGroupDefinition("/api", nil)

		handler := func(c *Context) error { return nil }
		group.AddRoute("GET", "/users/{id:[invalid(}", handler) // Invalid regex

		err := group.CommitToTree(tree, nil)
		if err == nil {
			t.Error("expected error for invalid regex pattern")
		}
	})

	t.Run("returns error when child group has invalid route", func(t *testing.T) {
		tree := NewTree()
		parent := NewGroupDefinition("/api", nil)
		child := parent.AddChild("/v1")

		handler := func(c *Context) error { return nil }
		child.AddRoute("GET", "/items/{id:[bad(}", handler) // Invalid regex in child

		err := parent.CommitToTree(tree, nil)
		if err == nil {
			t.Error("expected error from child group")
		}
	})
}
