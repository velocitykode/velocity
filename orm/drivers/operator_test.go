package drivers

import (
	"strings"
	"testing"
)

// TestOperatorRegistry_PerDriver pins the per-driver registry shape so a
// future change cannot silently widen or shrink the dialect-specific
// operator surface without touching this test.
func TestOperatorRegistry_PerDriver(t *testing.T) {
	t.Run("postgres registers JSONB / FTS / array overlap", func(t *testing.T) {
		d := &PostgresDriver{}
		reg := d.OperatorRegistry()
		if reg == nil {
			t.Fatal("postgres registry should not be nil")
		}
		want := []string{"@>", "<@", "?", "?|", "?&", "@@", "&&"}
		for _, op := range want {
			if _, ok := reg[op]; !ok {
				t.Errorf("postgres registry missing operator %q", op)
			}
		}
		// Ensure every spec carries a non-empty Template (compile renders it).
		for op, spec := range reg {
			if spec.Template == "" {
				t.Errorf("postgres operator %q has empty Template", op)
			}
			if spec.Op != op {
				t.Errorf("postgres operator %q stored under spec.Op=%q", op, spec.Op)
			}
		}
	})

	t.Run("sqlite returns nil", func(t *testing.T) {
		d := &SQLiteDriver{}
		if got := d.OperatorRegistry(); got != nil {
			t.Errorf("sqlite registry: got %v, want nil", got)
		}
	})

	t.Run("mysql returns nil", func(t *testing.T) {
		d := &MySQLDriver{}
		if got := d.OperatorRegistry(); got != nil {
			t.Errorf("mysql registry: got %v, want nil", got)
		}
	})
}

// TestRenderOperatorTemplate_Postgres pins the SQL fragment + bound-param
// shape for each registered postgres operator so a future grammar tweak
// surfaces here as a test diff.
func TestRenderOperatorTemplate_Postgres(t *testing.T) {
	g := &PostgresGrammar{}
	reg := (&PostgresDriver{}).OperatorRegistry()

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
			fragment, nextIdx := renderOperatorTemplate(g, cond, 1, &args, "$%d")

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
	fragment, _ := renderOperatorTemplate(g, cond, 1, &args, "?")

	if !strings.Contains(fragment, "?") {
		t.Errorf("fragment missing literal placeholder: %q", fragment)
	}
	if strings.Contains(fragment, "%!(EXTRA") {
		t.Errorf("fragment contains fmt-error sentinel: %q", fragment)
	}
}
