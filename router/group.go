package router

import "github.com/velocitykode/velocity/pipeline"

// RouteDefinition represents a route before it's committed to the tree
type RouteDefinition struct {
	Method      string
	Path        string
	Handler     HandlerFunc
	Middlewares []MiddlewareFunc
	Name        string
}

// GroupDefinition holds group configuration for deferred registration
type GroupDefinition struct {
	prefix      string
	middlewares []MiddlewareFunc
	routes      []*RouteDefinition
	children    []*GroupDefinition
	parent      *GroupDefinition
}

// NewGroupDefinition creates a new group definition
func NewGroupDefinition(prefix string, parent *GroupDefinition) *GroupDefinition {
	return &GroupDefinition{
		prefix:      prefix,
		routes:      make([]*RouteDefinition, 0),
		children:    make([]*GroupDefinition, 0),
		middlewares: make([]MiddlewareFunc, 0),
		parent:      parent,
	}
}

// AddRoute adds a route to the group
func (g *GroupDefinition) AddRoute(method, path string, handler HandlerFunc) *RouteDefinition {
	route := &RouteDefinition{
		Method:  method,
		Path:    path,
		Handler: handler,
	}
	g.routes = append(g.routes, route)
	return route
}

// AddChild creates and adds a child group
func (g *GroupDefinition) AddChild(prefix string) *GroupDefinition {
	child := NewGroupDefinition(prefix, g)
	g.children = append(g.children, child)
	return child
}

// Use adds middleware to the group
// Middleware is applied during CommitToTree, so it works whether called before or after routes
func (g *GroupDefinition) Use(middlewares ...MiddlewareFunc) {
	g.middlewares = append(g.middlewares, middlewares...)
}

// FullPrefix returns the complete prefix including parent prefixes
func (g *GroupDefinition) FullPrefix() string {
	if g.parent == nil {
		return g.prefix
	}
	parentPrefix := g.parent.FullPrefix()
	if parentPrefix == "" {
		return g.prefix
	}
	if g.prefix == "" {
		return parentPrefix
	}
	return parentPrefix + g.prefix
}

// CommitToTree adds all routes in this group and children to the tree
func (g *GroupDefinition) CommitToTree(tree *Tree, globalMiddlewares []MiddlewareFunc) error {
	fullPrefix := g.FullPrefix()

	// Collect all middleware from parent chain (root -> ... -> this group)
	inheritedMiddlewares := g.collectInheritedMiddlewares()

	for _, route := range g.routes {
		fullPath := fullPrefix + route.Path

		// Build complete middleware chain: global -> inherited -> own -> route
		allMiddlewares := make([]MiddlewareFunc, 0)
		allMiddlewares = append(allMiddlewares, globalMiddlewares...)
		allMiddlewares = append(allMiddlewares, inheritedMiddlewares...)
		allMiddlewares = append(allMiddlewares, g.middlewares...)
		allMiddlewares = append(allMiddlewares, route.Middlewares...)

		// Apply middleware to handler
		finalHandler := applyMiddlewareChain(route.Handler, allMiddlewares)

		// Insert into tree
		if err := tree.InsertWithName(route.Method, fullPath, finalHandler, route.Name); err != nil {
			return err
		}
	}

	// Commit children
	for _, child := range g.children {
		if err := child.CommitToTree(tree, globalMiddlewares); err != nil {
			return err
		}
	}

	return nil
}

// collectInheritedMiddlewares collects middleware from all parent groups
func (g *GroupDefinition) collectInheritedMiddlewares() []MiddlewareFunc {
	if g.parent == nil {
		return nil
	}

	// Get parent's inherited + own, recursively
	parentMiddlewares := g.parent.collectInheritedMiddlewares()
	result := make([]MiddlewareFunc, 0)
	result = append(result, parentMiddlewares...)
	result = append(result, g.parent.middlewares...)
	return result
}

// applyMiddlewareChain wraps handler with middleware in correct order
func applyMiddlewareChain(handler HandlerFunc, middlewares []MiddlewareFunc) HandlerFunc {
	if len(middlewares) == 0 {
		return handler
	}

	pipes := make([]pipeline.Stage[*Context], len(middlewares))
	for i, mw := range middlewares {
		mw := mw
		pipes[i] = pipeline.Pipe[*Context](func(c *Context, next func(*Context) error) error {
			wrapped := mw(func(ctx *Context) error {
				return next(ctx)
			})
			return wrapped(c)
		})
	}

	return func(c *Context) error {
		return pipeline.New[*Context]().
			Send(c).
			Through(pipes...).
			Then(func(ctx *Context) error {
				return handler(ctx)
			})
	}
}
