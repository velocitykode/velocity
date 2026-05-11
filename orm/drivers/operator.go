package drivers

import (
	"fmt"
	"strings"
)

// ParamShape constrains the Go type that an OperatorSpec accepts as its
// bound value. Drivers register operators with a ParamShape so the typed
// Where chain can validate the call site at parse time rather than letting
// a misuse surface as a SQL syntax error at execute time.
type ParamShape int

const (
	// ParamScalar accepts any single value (string, int, bool, []byte, ...).
	// Used for operators with Arity 1 that take a single bound parameter.
	ParamScalar ParamShape = iota

	// ParamSlice accepts a Go []any. Used for variadic operators (IN-style)
	// where the chain expands one placeholder per element.
	ParamSlice

	// ParamJSON accepts string, []byte, or json.RawMessage. The grammar is
	// expected to cast the placeholder to jsonb (or the dialect's JSON
	// type) inside the template; the user supplies the raw JSON text.
	ParamJSON

	// ParamArray accepts a []any rendered as a Postgres array literal. The
	// grammar expands one placeholder per element, the same way IN does,
	// but with array-aware bracketing in the template.
	ParamArray
)

// ArityVariadic marks an OperatorSpec whose value is a slice of unknown
// length. The grammar expands one placeholder per element when emitting
// the SQL.
const ArityVariadic = -1

// OperatorSpec describes a registered SQL operator that the typed Where
// chain admits in addition to the built-in scalar allowlist. Drivers
// register specs via Driver.OperatorRegistry; an unregistered operator
// keeps the existing "invalid SQL operator" rejection.
//
// Example: Postgres registers `@>` with ParamShape=ParamJSON and Template
// `{{lhs}} @> {{rhs}}::jsonb`, so a chain like
//
//	(App{}).Where("processes @> ?", `{"key":"value"}`).Get(ctx)
//
// emits `apps.processes @> $1::jsonb` instead of forcing the caller to
// fall back to Raw and lose the typed chain, scopes, and soft-delete
// filtering.
type OperatorSpec struct {
	// Op is the canonical operator string as the user types it ("@>",
	// "?|", "@@", "&&"). Lookup is case-insensitive.
	Op string

	// Arity is the number of bound parameters this operator consumes
	// from cond.Value. 1 = scalar (e.g. @>, ILIKE-like). 2 = pair
	// (BETWEEN-like). ArityVariadic = N (IN-like, slice-shaped).
	Arity int

	// ParamShape constrains the Go type of cond.Value at parse time.
	ParamShape ParamShape

	// Template is the SQL fragment the grammar emits. Three placeholders
	// are recognised:
	//   {{lhs}} - the column identifier (already quoted by the grammar)
	//   {{op}}  - the operator literal
	//   {{rhs}} - the bound-parameter placeholder ($N for postgres,
	//             ? for sqlite/mysql), already cast as needed
	// Example: "{{lhs}} @> {{rhs}}::jsonb"
	Template string
}

// renderOperatorTemplate compiles a Condition with cond.Spec set into a SQL
// fragment plus appended bound parameters. The grammar passes its quote
// helper, the next free placeholder index, and a placeholderFmt formatted
// the way the dialect emits parameters ("$%d" for postgres, "?" for the
// wildcard dialects). Returns the rendered fragment and the next free
// placeholder index.
//
// Variadic / slice / array shapes expand one placeholder per element so a
// single ?| call binds N parameters; ARRAY[...] bracketing in the template
// keeps the SQL syntactically valid for postgres array overlap.
func renderOperatorTemplate(g QueryGrammar, cond Condition, argIndex int, args *[]any, placeholderFmt string) (string, int) {
	spec := cond.Spec
	lhs := g.QuoteIdentifier(cond.Column)

	rhs, nextIdx := bindOperatorValue(spec, cond.Value, argIndex, args, placeholderFmt)

	out := spec.Template
	out = strings.ReplaceAll(out, "{{lhs}}", lhs)
	out = strings.ReplaceAll(out, "{{op}}", spec.Op)
	out = strings.ReplaceAll(out, "{{rhs}}", rhs)
	return out, nextIdx
}

// bindOperatorValue emits the right-hand-side placeholder fragment for a
// registered operator and appends bound parameters in shape-appropriate
// form. Scalar and JSON shapes consume one placeholder; slice / array
// shapes expand to a comma-separated list, with ARRAY[...] bracketing for
// ParamArray (the postgres array literal form).
func bindOperatorValue(spec *OperatorSpec, val any, argIndex int, args *[]any, placeholderFmt string) (string, int) {
	switch spec.ParamShape {
	case ParamScalar, ParamJSON:
		ph := renderPlaceholder(placeholderFmt, argIndex)
		*args = append(*args, val)
		return ph, argIndex + 1
	case ParamSlice, ParamArray:
		values, ok := val.([]any)
		if !ok || len(values) == 0 {
			ph := renderPlaceholder(placeholderFmt, argIndex)
			*args = append(*args, val)
			return ph, argIndex + 1
		}
		var parts []string
		for _, v := range values {
			parts = append(parts, renderPlaceholder(placeholderFmt, argIndex))
			argIndex++
			*args = append(*args, v)
		}
		joined := strings.Join(parts, ", ")
		if spec.ParamShape == ParamArray {
			return "ARRAY[" + joined + "]", argIndex
		}
		return "(" + joined + ")", argIndex
	}
	ph := renderPlaceholder(placeholderFmt, argIndex)
	*args = append(*args, val)
	return ph, argIndex + 1
}

// renderPlaceholder formats a single bound-parameter placeholder for the
// active dialect. Indexed dialects (postgres) pass a format string with a
// %d verb ("$%d") that resolves to "$1", "$2", ... Non-indexed dialects
// (mysql, sqlite) pass a literal placeholder ("?") that has no verb to
// substitute; passing such a literal through fmt.Sprintf with an extra int
// would emit "?%!(EXTRA int=N)" and corrupt the SQL the moment either
// driver registers an extension operator. Detect the no-verb case and
// return the placeholder verbatim.
func renderPlaceholder(placeholderFmt string, argIndex int) string {
	if strings.Contains(placeholderFmt, "%") {
		return fmt.Sprintf(placeholderFmt, argIndex)
	}
	return placeholderFmt
}
