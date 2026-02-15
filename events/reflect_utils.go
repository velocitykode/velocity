package events

import (
	"reflect"
	"strings"
)

// resolveEventName extracts the event name from various types using a consistent
// strategy: Event interface first, string second, then reflection-based type name.
// This consolidates the duplicated getEventName implementations across
// dispatcher.go, fake.go, middleware.go, and queue_integration.go.
func resolveEventName(event interface{}) string {
	if e, ok := event.(Event); ok {
		return e.Name()
	}

	if s, ok := event.(string); ok {
		return s
	}

	t := reflect.TypeOf(event)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	name := t.Name()
	if name == "" {
		name = t.String()
	}

	return camelToDot(name)
}

// resolveTypeName returns the fully-qualified type name of a value,
// dereferencing pointers. This consolidates the duplicated type extraction
// pattern used in FakeDispatcher assertion methods.
func resolveTypeName(v interface{}) string {
	t := reflect.TypeOf(v)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t.String()
}

// camelToDot converts CamelCase to dot.separated.lowercase.
// This consolidates the duplicate camelToSnake/extractEventName implementations
// in dispatcher.go, subscriber.go, and discovery.go.
func camelToDot(s string) string {
	var result strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			result.WriteRune('.')
		}
		result.WriteRune(r)
	}
	return strings.ToLower(result.String())
}
