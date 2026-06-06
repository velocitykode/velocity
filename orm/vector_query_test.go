package orm

import (
	"reflect"
	"testing"

	"github.com/velocitykode/velocity/orm/drivers"
)

// vecTestDriver is a minimal drivers.Driver whose only meaningful behaviour is
// the grammar it exposes, so the vector query builder can be exercised without a
// live database. Methods other than Grammar/DriverName are never called by the
// builder path and would panic on the embedded nil interface if they were.
type vecTestDriver struct {
	drivers.Driver
	grammar drivers.QueryGrammar
	name    string
}

func (d vecTestDriver) Grammar() drivers.QueryGrammar                     { return d.grammar }
func (d vecTestDriver) DriverName() string                                { return d.name }
func (d vecTestDriver) OperatorRegistry() map[string]drivers.OperatorSpec { return nil }

func pgVecQuery() *Query[User] {
	return &Query[User]{
		driver: vecTestDriver{grammar: &drivers.PostgresGrammar{}, name: "postgres"},
		table:  "documents",
	}
}

func TestOrderByDistance_BuildsExprOrder(t *testing.T) {
	q := pgVecQuery().OrderByDistance("embedding", Vector{1, 2, 3}, DistanceCosine)
	if q.err != nil {
		t.Fatalf("unexpected err: %v", q.err)
	}
	if len(q.orders) != 1 {
		t.Fatalf("orders = %d, want 1", len(q.orders))
	}
	got := q.orders[0]
	if want := `"embedding" <=> ?::vector`; got.Expr != want {
		t.Errorf("Expr = %q, want %q", got.Expr, want)
	}
	if got.Direction != "ASC" {
		t.Errorf("Direction = %q, want ASC", got.Direction)
	}
	if got.Column != "" {
		t.Errorf("Column = %q, want empty (expr order)", got.Column)
	}
	if len(got.Args) != 1 {
		t.Fatalf("Args = %d, want 1", len(got.Args))
	}
	if vec, ok := got.Args[0].(Vector); !ok || !reflect.DeepEqual(vec, Vector{1, 2, 3}) {
		t.Errorf("Args[0] = %#v, want Vector{1,2,3}", got.Args[0])
	}
}

func TestOrderByDistance_MetricsMapToOperators(t *testing.T) {
	cases := map[DistanceMetric]string{
		DistanceL2:           `"e" <-> ?::vector`,
		DistanceCosine:       `"e" <=> ?::vector`,
		DistanceInnerProduct: `"e" <#> ?::vector`,
		DistanceL1:           `"e" <+> ?::vector`,
	}
	for metric, want := range cases {
		q := pgVecQuery().OrderByDistance("e", Vector{1}, metric)
		if q.err != nil {
			t.Fatalf("metric %q: unexpected err: %v", metric, q.err)
		}
		if q.orders[0].Expr != want {
			t.Errorf("metric %q: Expr = %q, want %q", metric, q.orders[0].Expr, want)
		}
	}
}

func TestNearestNeighbors_SetsLimit(t *testing.T) {
	q := pgVecQuery().NearestNeighbors("embedding", Vector{1, 2}, DistanceL2, 10)
	if q.err != nil {
		t.Fatalf("unexpected err: %v", q.err)
	}
	if q.limit == nil || *q.limit != 10 {
		t.Errorf("limit = %v, want 10", q.limit)
	}
	if len(q.orders) != 1 || q.orders[0].Expr == "" {
		t.Errorf("expected one expr order, got %+v", q.orders)
	}
}

func TestOrderByDistance_UnsupportedDriverErrors(t *testing.T) {
	q := &Query[User]{
		driver: vecTestDriver{grammar: &drivers.SQLiteGrammar{}, name: "sqlite"},
		table:  "documents",
	}
	q = q.OrderByDistance("embedding", Vector{1, 2, 3}, DistanceCosine)
	if q.err == nil {
		t.Fatal("expected capability error on non-vector driver, got nil")
	}
	if len(q.orders) != 0 {
		t.Errorf("orders should not be appended on error, got %d", len(q.orders))
	}
}

func TestOrderByDistance_RejectsBadInputs(t *testing.T) {
	t.Run("invalid metric", func(t *testing.T) {
		q := pgVecQuery().OrderByDistance("embedding", Vector{1}, DistanceMetric("bogus"))
		if q.err == nil {
			t.Fatal("expected error for unknown metric")
		}
	})
	t.Run("invalid column", func(t *testing.T) {
		q := pgVecQuery().OrderByDistance("embedding; DROP TABLE x", Vector{1}, DistanceCosine)
		if q.err == nil {
			t.Fatal("expected error for invalid identifier")
		}
	})
}

func TestSelectDistance_ProjectsScoreAndKeepsStar(t *testing.T) {
	q := pgVecQuery().SelectDistance("embedding", Vector{1, 2, 3}, DistanceCosine, "distance")
	if q.err != nil {
		t.Fatalf("unexpected err: %v", q.err)
	}
	if len(q.rawColumns) != 1 {
		t.Fatalf("rawColumns = %d, want 1", len(q.rawColumns))
	}
	if want := `"embedding" <=> ?::vector AS "distance"`; q.rawColumns[0].Expr != want {
		t.Errorf("Expr = %q, want %q", q.rawColumns[0].Expr, want)
	}
	// Model columns must still be selected so the score does not displace them.
	if len(q.columns) != 1 || q.columns[0] != "*" {
		t.Errorf("columns = %v, want [*]", q.columns)
	}
}

// End-to-end: OrderByDistance + a WHERE filter must compile to a single SELECT
// with contiguous placeholder numbering ($1 for WHERE, $2 for the vector in
// ORDER BY) and the bound args in the matching order.
func TestOrderByDistance_CompilesToSQL(t *testing.T) {
	q := pgVecQuery()
	q.conditions = append(q.conditions, drivers.Condition{Column: "active", Operator: "=", Value: true, Type: "and"})
	q = q.OrderByDistance("embedding", Vector{1, 2, 3}, DistanceCosine).Limit(5)
	if q.err != nil {
		t.Fatalf("unexpected err: %v", q.err)
	}

	sq := &drivers.SelectQuery{
		Table:      q.table,
		Conditions: q.conditions,
		Orders:     q.orders,
		Limit:      q.limit,
	}
	sql, args := q.driver.Grammar().CompileSelect(sq)

	want := `SELECT * FROM "documents" WHERE "active" = $1 ORDER BY "embedding" <=> $2::vector ASC LIMIT 5`
	if sql != want {
		t.Errorf("SQL = %q\nwant %q", sql, want)
	}
	if len(args) != 2 {
		t.Fatalf("args = %v, want 2", args)
	}
	if args[0] != true {
		t.Errorf("args[0] = %#v, want true", args[0])
	}
	if vec, ok := args[1].(Vector); !ok || !reflect.DeepEqual(vec, Vector{1, 2, 3}) {
		t.Errorf("args[1] = %#v, want Vector{1,2,3}", args[1])
	}
}
