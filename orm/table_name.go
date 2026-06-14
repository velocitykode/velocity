package orm

import (
	"reflect"
	"sync"

	"github.com/velocitykode/velocity/str"
)

// tableNameCache memoizes the resolved table name per concrete (deref'd)
// reflect.Type. The derivation walks the method set and, for a custom
// TableName(), allocates a receiver and calls it on every newQuery/save;
// the result is invariant for a given type, so it is cached the same way
// MetaFor caches ModelMeta. Holds the FINAL string, including any
// TableName() override result.
//
// The map value is a *tableNameEntry whose sync.Once serializes the miss
// path, so concurrent cold misses for the same type run resolveTableName
// (and thus any custom TableName()) exactly once.
var tableNameCache sync.Map // map[reflect.Type]*tableNameEntry

// tableNameEntry holds the once-derived table name for a single type.
type tableNameEntry struct {
	once sync.Once
	name string
}

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

	v, ok := tableNameCache.Load(t)
	if !ok {
		v, _ = tableNameCache.LoadOrStore(t, &tableNameEntry{})
	}
	entry := v.(*tableNameEntry)
	entry.once.Do(func() {
		entry.name = resolveTableName(t)
	})
	return entry.name
}

// resolveTableName performs the uncached derivation for an already-deref'd
// type t. Split out so deriveTableName owns the cache lookup/store.
func resolveTableName(t reflect.Type) string {
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
