package orm

import (
	"reflect"

	"github.com/velocitykode/velocity/str"
)

// deriveTableName resolves the table name for a model type. It honors a
// TableName() string method declared on EITHER the value or the pointer
// receiver, and otherwise falls back to str.Plural(ToSnakeCase(typeName)).
//
// This is the single canonical derivation shared by the read path
// (getTableName), the write path (saveWithDriver) and the relation
// resolver (resolveTableNameReflect), so a given model resolves to the
// SAME table no matter which path reaches it. It also matches the
// scaffolder's toTableName (console/make_model.go), so a generated model's
// runtime table equals the one the generator named.
//
// BREAKING (vs. the historical per-path conventions): the fallback now
// produces snake_case, properly-pluralized names. A multi-word model like
// UserProfile derives "user_profiles" (the read path previously produced
// "userprofiles", disagreeing with the write path's "user_profiles"), and
// irregular plurals follow str.Plural (Category -> "categories",
// Box -> "boxes"). To pin a legacy table name, override TableName():
//
//	func (UserProfile) TableName() string { return "userprofiles" }
//
// Returns "" for a nil type or a type with no name (e.g. an anonymous
// generic instantiation); callers supply their own default for that case.
func deriveTableName(t reflect.Type) string {
	if t == nil {
		return ""
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	// Value receiver first (most common for TableName), then pointer
	// receiver, so a method declared with either receiver set wins over
	// the naming convention.
	if name, ok := callTableName(t); ok {
		return name
	}
	if name, ok := callTableName(reflect.PointerTo(t)); ok {
		return name
	}

	name := t.Name()
	if name == "" {
		return ""
	}
	return str.Plural(ToSnakeCase(name))
}

// callTableName invokes a no-arg TableName() string method on recv (a value
// or pointer type), returning the name and true when the method exists, has
// the expected signature, and yields a non-empty string.
func callTableName(recv reflect.Type) (string, bool) {
	method, ok := recv.MethodByName("TableName")
	if !ok {
		return "", false
	}
	if method.Type.NumIn() != 1 || method.Type.NumOut() != 1 || method.Type.Out(0).Kind() != reflect.String {
		return "", false
	}
	var receiver reflect.Value
	if recv.Kind() == reflect.Ptr {
		receiver = reflect.New(recv.Elem())
	} else {
		receiver = reflect.New(recv).Elem()
	}
	result := method.Func.Call([]reflect.Value{receiver})
	if name, ok := result[0].Interface().(string); ok && name != "" {
		return name, true
	}
	return "", false
}
