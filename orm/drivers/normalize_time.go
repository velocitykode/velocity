package drivers

import (
	"database/sql"
	"database/sql/driver"
	"reflect"
	"time"
)

// NormalizeTimeArgs rebases every time value in args to UTC so the wall
// clock written into naive (no-timezone) columns never depends on the
// writer's process timezone. This is the framework's storage contract:
// instants are stored UTC; zones are presentation.
//
// Conversions applied:
//   - time.Time            -> t.In(time.UTC)
//   - *time.Time (non-nil) -> new pointer to the UTC value; the caller's
//     pointee is never mutated (it may be a live struct field)
//   - sql.NullTime (Valid) -> copy with the Time rebased to UTC
//   - sql.NamedArg         -> copy with its Value rebased by the same
//     rules (Name preserved), so named parameters cannot smuggle a local
//     wall clock past the choke point
//   - driver.Valuer whose Value() yields a non-UTC time.Time -> the
//     resolved time rebased to UTC (database/sql would have resolved the
//     Valuer to that same time anyway; substituting the rebased result is
//     equivalent). A Valuer that errors, is a typed nil pointer, or yields
//     anything other than a time.Time passes through untouched for
//     database/sql to handle.
//
// Everything else passes through untouched. timestamptz/TIMESTAMP-WITH-
// TIME-ZONE columns are instant-preserving, so the rebase is a no-op for
// them by construction.
//
// Copy-on-write: the input slice is returned unchanged (no allocation)
// when no rebase is needed.
func NormalizeTimeArgs(args []any) []any {
	out := args
	cloned := false
	for i, arg := range args {
		rebased, ok := rebaseTimeValue(arg)
		if !ok {
			continue
		}
		if !cloned {
			out = make([]any, len(args))
			copy(out, args)
			cloned = true
		}
		out[i] = rebased
	}
	return out
}

// rebaseTimeValue applies the NormalizeTimeArgs conversion rules to a
// single value. It reports false when the value needs no rebase (already
// UTC, nil, or not time-shaped), so callers can preserve the original.
func rebaseTimeValue(arg any) (any, bool) {
	switch v := arg.(type) {
	case time.Time:
		if v.Location() == time.UTC {
			return nil, false
		}
		return v.In(time.UTC), true
	case *time.Time:
		if v == nil || v.Location() == time.UTC {
			return nil, false
		}
		u := v.In(time.UTC)
		return &u, true
	case sql.NullTime:
		if !v.Valid || v.Time.Location() == time.UTC {
			return nil, false
		}
		return sql.NullTime{Time: v.Time.In(time.UTC), Valid: true}, true
	case sql.NamedArg:
		inner, ok := rebaseTimeValue(v.Value)
		if !ok {
			return nil, false
		}
		// v is already a copy (type-switch binds by value); replace only
		// Value so every other NamedArg field survives untouched.
		v.Value = inner
		return v, true
	default:
		t, ok := timeFromValuer(arg)
		if !ok || t.Location() == time.UTC {
			return nil, false
		}
		return t.In(time.UTC), true
	}
}

// timeFromValuer resolves arg's driver.Valuer, if any, and reports whether
// it yields a time.Time (custom time wrappers would otherwise smuggle a
// local wall clock past the rebase). Mirrors database/sql's nil-pointer
// guard: a typed nil Valuer is left for database/sql to resolve to NULL
// rather than risking a panic on a nil receiver here.
func timeFromValuer(arg any) (time.Time, bool) {
	valuer, ok := arg.(driver.Valuer)
	if !ok {
		return time.Time{}, false
	}
	if rv := reflect.ValueOf(valuer); rv.Kind() == reflect.Pointer && rv.IsNil() {
		return time.Time{}, false
	}
	dv, err := valuer.Value()
	if err != nil {
		return time.Time{}, false
	}
	t, ok := dv.(time.Time)
	return t, ok
}
