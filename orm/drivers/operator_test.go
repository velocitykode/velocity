package drivers

import (
	"strings"
	"testing"
)

// postgresTestOperators mirrors the spec set the orm/postgres leaf registers
// via PostgresDriver.OperatorRegistry. It is duplicated here as a fixture so
// the renderOperatorTemplate behaviour (unexported, must be exercised from
// inside package drivers) can be pinned against postgres-shaped specs without
// importing the leaf. The leaf has its own test asserting the live registry
// shape matches.
var postgresTestOperators = map[string]OperatorSpec{
	"@>": {Op: "@>", Arity: 1, ParamShape: ParamJSON, Template: "{{lhs}} @> {{rhs}}::jsonb"},
	"<@": {Op: "<@", Arity: 1, ParamShape: ParamJSON, Template: "{{lhs}} <@ {{rhs}}::jsonb"},
	"?":  {Op: "?", Arity: 1, ParamShape: ParamScalar, Template: "{{lhs}} ? {{rhs}}"},
	"?|": {Op: "?|", Arity: 1, ParamShape: ParamArray, Template: "{{lhs}} ?| {{rhs}}"},
	"?&": {Op: "?&", Arity: 1, ParamShape: ParamArray, Template: "{{lhs}} ?& {{rhs}}"},
	"@@": {Op: "@@", Arity: 1, ParamShape: ParamScalar, Template: "{{lhs}} @@ to_tsquery({{rhs}})"},
	"&&": {Op: "&&", Arity: 1, ParamShape: ParamArray, Template: "{{lhs}} && {{rhs}}"},
}

// TestOperatorRegistry_SQLiteNil pins that the SQLite driver (whose connector
// stays in this package) declares no extension operators. The postgres and
// mysql registry-shape assertions live with their connectors in the
// orm/postgres and orm/mysql leaf packages.
func TestOperatorRegistry_SQLiteNil(t *testing.T) {
	d := &SQLiteDriver{}
	if got := d.OperatorRegistry(); got != nil {
		t.Errorf("sqlite registry: got %v, want nil", got)
	}
}

// TestRenderOperatorTemplate_Postgres pins the SQL fragment + bound-param
// shape for each postgres operator so a future grammar tweak surfaces here as
// a test diff. It uses the PostgresGrammar (which stays in package drivers)
// and the postgresTestOperators fixture.
func TestRenderOperatorTemplate_Postgres(t *testing.T) {
	g := &PostgresGrammar{}
	reg := postgresTestOperators

	tests := []struct {
		name     string
		op       string
		val      any
		wantSQL  string
		wantArgs []any
	}{
		{
			name:     "@> JSONB containment",
			op:       "@>",
			val:      `{"key":"value"}`,
			wantSQL:  `"processes" @> $1::jsonb`,
			wantArgs: []any{`{"key":"value"}`},
		},
		{
			name:     "<@ JSONB contained",
			op:       "<@",
			val:      `{"key":"value"}`,
			wantSQL:  `"processes" <@ $1::jsonb`,
			wantArgs: []any{`{"key":"value"}`},
		},
		{
			name:     "?| any-key existence",
			op:       "?|",
			val:      []any{"a", "b"},
			wantSQL:  `"processes" ?| ARRAY[$1, $2]`,
			wantArgs: []any{"a", "b"},
		},
		{
			name:     "@@ FTS",
			op:       "@@",
			val:      "go & lang",
			wantSQL:  `"body" @@ to_tsquery($1)`,
			wantArgs: []any{"go & lang"},
		},
		{
			name:     "&& array overlap",
			op:       "&&",
			val:      []any{1, 2, 3},
			wantSQL:  `"tags" && ARRAY[$1, $2, $3]`,
			wantArgs: []any{1, 2, 3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, ok := reg[tt.op]
			if !ok {
				t.Fatalf("operator %q not registered", tt.op)
			}
			column := "processes"
			if tt.op == "@@" {
				column = "body"
			}
			if tt.op == "&&" {
				column = "tags"
			}
			cond := Condition{Column: column, Operator: tt.op, Value: tt.val, Spec: &spec}
			args := []any{}
			fragment, nextIdx := renderOperatorTemplate(g, cond, 1, &args, dollarPlaceholder)

			if fragment != tt.wantSQL {
				t.Errorf("SQL: got %q, want %q", fragment, tt.wantSQL)
			}
			if len(args) != len(tt.wantArgs) {
				t.Fatalf("args length: got %d, want %d", len(args), len(tt.wantArgs))
			}
			for i := range args {
				if args[i] != tt.wantArgs[i] {
					t.Errorf("args[%d]: got %v, want %v", i, args[i], tt.wantArgs[i])
				}
			}
			if nextIdx != 1+len(tt.wantArgs) {
				t.Errorf("nextIdx: got %d, want %d", nextIdx, 1+len(tt.wantArgs))
			}
		})
	}
}

// TestRenderOperatorTemplate_NonIndexedPlaceholder guards against a latent
// bug where bindOperatorValue ran the literal "?" placeholder used by
// mysql / sqlite through fmt.Sprintf with an extra int arg, producing
// "?%!(EXTRA int=N)" and corrupting the emitted SQL. The grammar callsite
// passes "?" verbatim as placeholderFmt; the rendered fragment must
// contain a literal "?" and no fmt-error sentinel.
func TestRenderOperatorTemplate_NonIndexedPlaceholder(t *testing.T) {
	g := &SQLiteGrammar{}
	spec := OperatorSpec{
		Op:         "@>",
		Arity:      1,
		ParamShape: ParamScalar,
		Template:   "{{lhs}} {{op}} {{rhs}}",
	}
	cond := Condition{Column: "processes", Operator: "@>", Value: "x", Spec: &spec}
	args := []any{}
	fragment, _ := renderOperatorTemplate(g, cond, 1, &args, questionPlaceholder)

	if !strings.Contains(fragment, "?") {
		t.Errorf("fragment missing literal placeholder: %q", fragment)
	}
	if strings.Contains(fragment, "%!(EXTRA") {
		t.Errorf("fragment contains fmt-error sentinel: %q", fragment)
	}
}
