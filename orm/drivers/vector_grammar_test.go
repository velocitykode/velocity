package drivers

import (
	"reflect"
	"testing"
)

// PostgresGrammar must satisfy the optional VectorGrammar capability; the
// non-vector dialects must not, so the query builder's capability check fails
// closed for them.
func TestPostgresGrammar_ImplementsVectorGrammar(t *testing.T) {
	var _ VectorGrammar = (*PostgresGrammar)(nil)
}

func TestSQLiteAndMySQLGrammar_DoNotImplementVectorGrammar(t *testing.T) {
	if _, ok := any(&SQLiteGrammar{}).(VectorGrammar); ok {
		t.Error("SQLiteGrammar must not satisfy VectorGrammar; the builder relies on the negative case to error")
	}
	if _, ok := any(&MySQLGrammar{}).(VectorGrammar); ok {
		t.Error("MySQLGrammar must not satisfy VectorGrammar; the builder relies on the negative case to error")
	}
}

func TestPostgresGrammar_VectorDistanceExpr(t *testing.T) {
	g := &PostgresGrammar{}
	tests := []struct {
		metric string
		want   string
	}{
		{"l2", `"embedding" <-> ?::vector`},
		{"euclidean", `"embedding" <-> ?::vector`},
		{"cosine", `"embedding" <=> ?::vector`},
		{"inner_product", `"embedding" <#> ?::vector`},
		{"ip", `"embedding" <#> ?::vector`},
		{"l1", `"embedding" <+> ?::vector`},
		{"COSINE", `"embedding" <=> ?::vector`}, // case-insensitive
	}
	for _, tt := range tests {
		t.Run(tt.metric, func(t *testing.T) {
			got, err := g.VectorDistanceExpr(g.QuoteIdentifier("embedding"), tt.metric)
			if err != nil {
				t.Fatalf("VectorDistanceExpr(%q) error = %v", tt.metric, err)
			}
			if got != tt.want {
				t.Errorf("VectorDistanceExpr(%q) = %q, want %q", tt.metric, got, tt.want)
			}
		})
	}
}

func TestPostgresGrammar_VectorDistanceExpr_UnknownMetric(t *testing.T) {
	g := &PostgresGrammar{}
	if _, err := g.VectorDistanceExpr(`"embedding"`, "hamming"); err == nil {
		t.Fatal("expected error for unsupported metric, got nil")
	}
}

// A raw-expression Order must compile after WHERE with contiguous $N numbering
// and append its args to the stream in order.
func TestPostgresGrammar_CompileSelect_ExprOrder(t *testing.T) {
	g := &PostgresGrammar{}
	limit := 5
	query := &SelectQuery{
		Table:      "documents",
		Conditions: []Condition{{Column: "active", Operator: "=", Value: true, Type: "and"}},
		Orders:     []Order{{Expr: `"embedding" <=> ?::vector`, Args: []any{"[1,2,3]"}, Direction: "ASC"}},
		Limit:      &limit,
	}
	gotSQL, gotArgs := g.CompileSelect(query)
	wantSQL := `SELECT * FROM "documents" WHERE "active" = $1 ORDER BY "embedding" <=> $2::vector ASC LIMIT 5`
	if gotSQL != wantSQL {
		t.Errorf("SQL = %q\nwant %q", gotSQL, wantSQL)
	}
	wantArgs := []any{true, "[1,2,3]"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Errorf("args = %#v, want %#v", gotArgs, wantArgs)
	}
}

// A plain column Order keeps working unchanged alongside the new Expr path.
func TestPostgresGrammar_CompileSelect_MixedOrders(t *testing.T) {
	g := &PostgresGrammar{}
	query := &SelectQuery{
		Table: "documents",
		Orders: []Order{
			{Column: "created_at", Direction: "DESC"},
			{Expr: `"embedding" <-> ?::vector`, Args: []any{"[9]"}, Direction: "ASC"},
		},
	}
	gotSQL, gotArgs := g.CompileSelect(query)
	wantSQL := `SELECT * FROM "documents" ORDER BY "created_at" DESC, "embedding" <-> $1::vector ASC`
	if gotSQL != wantSQL {
		t.Errorf("SQL = %q\nwant %q", gotSQL, wantSQL)
	}
	if !reflect.DeepEqual(gotArgs, []any{"[9]"}) {
		t.Errorf("args = %#v, want [\"[9]\"]", gotArgs)
	}
}
