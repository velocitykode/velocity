// Package nilval answers one question the framework asks wherever it
// refuses a nil value from user code: is this value nil, as an interface
// or as a nil value of a nillable type behind one (a typed nil pointer)?
//
// A typed nil passes a plain v == nil check, so a framework entry point
// that refused only the untyped nil would accept it and later hand user
// code a nil receiver. Every such entry point uses Is, so a typed nil is
// nil everywhere the framework refuses nil. It imports only the standard
// library and does not allocate.
package nilval

import "reflect"

// Is reports whether v is nil: an untyped nil, or a nil pointer, map,
// slice, func, channel or interface held in v.
func Is(v any) bool {
	if v == nil {
		return true
	}
	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface:
		return rv.IsNil()
	}
	return false
}
