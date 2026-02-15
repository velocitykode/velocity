package events

import (
	"reflect"
	"strings"
)

// resolveEventName extracts the event name from various types.
// For types implementing Event, returns Event.Name().
// For strings, returns the string as-is.
// For other types, derives the name from the type using camelToDot conversion
// (e.g., UserRegistered -> user.registered).
// Used by DefaultDispatcher and middleware where dot-notation is expected.
func resolveEventName(event interface{}) string {
	if e, ok := event.(Event); ok {
		return e.Name()
	}

	if s, ok := event.(string); ok {
		return s
	}

	name := reflectTypeName(event)
	return camelToDot(name)
}

// resolveEventNameRaw extracts the event name without case conversion.
// For types implementing Event, returns Event.Name().
// For strings, returns the string as-is.
// For other types, returns the raw type name (e.g., "NamedType").
// Used by FakeDispatcher where raw type names are expected.
func resolveEventNameRaw(event interface{}) string {
	if e, ok := event.(Event); ok {
		return e.Name()
	}

	if s, ok := event.(string); ok {
		return s
	}

	return reflectTypeName(event)
}

// reflectTypeName returns the short type name of a value, dereferencing pointers.
func reflectTypeName(v interface{}) string {
	t := reflect.TypeOf(v)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	name := t.Name()
	if name == "" {
		name = t.String()
	}
	return name
}

// resolveTypeName returns the fully-qualified type name of a value,
// dereferencing pointers. Used by FakeDispatcher assertion methods.
func resolveTypeName(v interface{}) string {
	t := reflect.TypeOf(v)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t.String()
}

// camelToDot converts CamelCase to dot.separated.lowercase
// (e.g., UserRegistered -> user.registered).
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
