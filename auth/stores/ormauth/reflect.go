package ormauth

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"

	"github.com/velocitykode/velocity/orm"
)

// nullStringType is the reflect.Type of sql.NullString, accepted as a
// column carrier so a nullable remember_token can be modelled without
// pointers.
var nullStringType = reflect.TypeOf(sql.NullString{})

// errUnsupportedFieldType is returned by stringFieldKind for a column
// whose Go field cannot carry a string value.
var errUnsupportedFieldType = errors.New("field must be string, *string, or sql.NullString")

// columnNames lists a model's columns for use in startup diagnostics.
func columnNames(meta *orm.ModelMeta) []string {
	cols := meta.Columns()
	names := make([]string, 0, len(cols))
	for _, col := range cols {
		names = append(names, col.Column)
	}
	return names
}

// implicitDeny reports whether model resolves to the ORM's deny-by-default
// mass-assignment policy: no AssignableFields(), no ProtectedFields(), and no
// AllowAllColumns() opt-in. It mirrors orm.PolicyFor's own branch rather
// than calling it, because the resulting flag is unexported and the
// distinction matters: a model with a declared policy is never policed by
// Query.Update, so a declared policy that happens to omit the
// remember-token column is still a working configuration.
func implicitDeny(model any) bool {
	if _, ok := model.(orm.Assignable); ok {
		return false
	}
	if _, ok := model.(orm.Protected); ok {
		return false
	}
	if open, ok := model.(orm.AllowAllColumns); ok && open.AllowAllColumns() {
		return false
	}
	return true
}

// stringFieldKind verifies that col's Go field can carry a string.
func stringFieldKind(meta *orm.ModelMeta, col orm.ColumnDef) error {
	field := meta.Type.FieldByIndex(col.IndexPath)
	if !stringCarrier(field.Type) {
		return fmt.Errorf("%w (got %s)", errUnsupportedFieldType, field.Type)
	}
	return nil
}

// stringCarrier reports whether t is one of the accepted carriers.
func stringCarrier(t reflect.Type) bool {
	switch {
	case t.Kind() == reflect.String:
		return true
	case t.Kind() == reflect.Ptr && t.Elem().Kind() == reflect.String:
		return true
	case t == nullStringType:
		return true
	default:
		return false
	}
}

// readString extracts the string value of an accepted carrier. A NULL
// column (nil pointer, invalid NullString) reads as the empty string,
// matching the sql.NullString handling the previous raw-SQL user store
// applied to remember_token.
func readString(v reflect.Value) (string, error) {
	switch {
	case v.Kind() == reflect.String:
		return v.String(), nil
	case v.Kind() == reflect.Ptr && v.Type().Elem().Kind() == reflect.String:
		if v.IsNil() {
			return "", nil
		}
		return v.Elem().String(), nil
	case v.Type() == nullStringType:
		ns := v.Interface().(sql.NullString)
		if !ns.Valid {
			return "", nil
		}
		return ns.String, nil
	default:
		return "", fmt.Errorf("%w (got %s)", errUnsupportedFieldType, v.Type())
	}
}

// writeString stores s into an accepted carrier. A field that cannot be
// set is left untouched: the value is a best-effort mirror of what was
// persisted, never the source of truth.
func writeString(v reflect.Value, s string) {
	if !v.CanSet() {
		return
	}
	switch {
	case v.Kind() == reflect.String:
		v.SetString(s)
	case v.Kind() == reflect.Ptr && v.Type().Elem().Kind() == reflect.String:
		p := reflect.New(v.Type().Elem())
		p.Elem().SetString(s)
		v.Set(p)
	case v.Type() == nullStringType:
		v.Set(reflect.ValueOf(sql.NullString{String: s, Valid: true}))
	}
}
