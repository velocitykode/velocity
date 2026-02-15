package router

import (
	"reflect"
	"strings"
)

// ResourceController defines the interface for a resource controller
// Controllers can implement any subset of these methods
type ResourceController interface{}

// ResourceConfig maps HTTP verbs to controller methods
type ResourceConfig struct {
	HttpMethod string
	Action     string
	PathSuffix string
	HasParam   bool
}

// DefaultResourceConfig returns the default REST resource configuration
func DefaultResourceConfig() []ResourceConfig {
	return []ResourceConfig{
		{"GET", "Index", "", false},
		{"GET", "Create", "/create", false},
		{"POST", "Store", "", false},
		{"GET", "Show", "/{id}", true},
		{"GET", "Edit", "/{id}/edit", true},
		{"PUT", "Update", "/{id}", true},
		{"DELETE", "Destroy", "/{id}", true},
	}
}

// registerResourceRoutes registers resource routes using reflection
// Routes are added directly to the tree since this is called during commitOnce
func registerResourceRoutes(router *VelocityRouterV2, basePath string, controller interface{}, methods map[string]bool, globalMiddlewares []MiddlewareFunc) {
	controllerValue := reflect.ValueOf(controller)
	controllerType := controllerValue.Type()

	configs := DefaultResourceConfig()

	for _, config := range configs {
		actionLower := strings.ToLower(config.Action)
		enabled := methods[actionLower]
		if !enabled {
			continue
		}

		// Check if controller has this method
		method, exists := controllerType.MethodByName(config.Action)
		if !exists {
			continue
		}

		// Verify method signature: func(c *Context) error
		if !isValidHandlerSignature(method.Type) {
			continue
		}

		// Build handler using reflection
		handler := buildReflectionHandler(controllerValue, method)

		// Apply global middleware
		finalHandler := applyMiddlewareChain(handler, globalMiddlewares)

		// Build full path
		fullPath := basePath + config.PathSuffix

		// Register directly to tree
		router.tree.Load().Insert(config.HttpMethod, fullPath, finalHandler)
	}
}

// isValidHandlerSignature checks if method has signature func(*Context) error
func isValidHandlerSignature(methodType reflect.Type) bool {
	// Method is bound to receiver, so:
	// NumIn() == 2: receiver + *Context
	// NumOut() == 1: error
	if methodType.NumIn() != 2 || methodType.NumOut() != 1 {
		return false
	}

	// Check input is *Context
	contextType := reflect.TypeOf((*Context)(nil))
	if methodType.In(1) != contextType {
		return false
	}

	// Check output is error
	errorType := reflect.TypeOf((*error)(nil)).Elem()
	if !methodType.Out(0).Implements(errorType) {
		return false
	}

	return true
}

// buildReflectionHandler creates a HandlerFunc from a reflected method
func buildReflectionHandler(controller reflect.Value, method reflect.Method) HandlerFunc {
	return func(c *Context) error {
		args := []reflect.Value{controller, reflect.ValueOf(c)}
		results := method.Func.Call(args)

		// Extract error from result
		if !results[0].IsNil() {
			return results[0].Interface().(error)
		}
		return nil
	}
}

// registerWithMiddlewares registers a resource with the given middleware chain
func (rr *resourceWrapperV2) registerWithMiddlewares(middlewares []MiddlewareFunc) {
	if rr.registered {
		return
	}
	rr.registered = true
	registerResourceRoutes(rr.router, rr.path, rr.controller, rr.methods, middlewares)
}
