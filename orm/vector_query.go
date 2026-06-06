package orm

import (
	"fmt"

	"github.com/velocitykode/velocity/orm/drivers"
)

// DistanceMetric names a vector distance function for similarity search. The
// concrete SQL operator is resolved by the driver's VectorGrammar, so the set
// of supported metrics is dialect-defined; these constants cover the metrics
// every pgvector build provides.
type DistanceMetric string

const (
	// DistanceL2 is Euclidean (L2) distance: pgvector "<->".
	DistanceL2 DistanceMetric = "l2"
	// DistanceCosine is cosine distance: pgvector "<=>".
	DistanceCosine DistanceMetric = "cosine"
	// DistanceInnerProduct is negative inner product: pgvector "<#>".
	DistanceInnerProduct DistanceMetric = "inner_product"
	// DistanceL1 is taxicab (L1) distance: pgvector "<+>" (pgvector 0.7+).
	DistanceL1 DistanceMetric = "l1"
)

// vectorGrammar resolves the active driver's VectorGrammar capability, or
// returns a clear error when the dialect does not support vector search. A
// vector query has no meaningful fallback on a non-vector driver, so callers
// surface this through the deferred-error path rather than degrading.
func (q *Query[T]) vectorGrammar(op string) (drivers.QueryGrammar, drivers.VectorGrammar, bool) {
	if q.driver == nil {
		q.setErr(op, fmt.Errorf("no database driver configured"))
		return nil, nil, false
	}
	grammar := q.driver.Grammar()
	vg, ok := grammar.(drivers.VectorGrammar)
	if !ok {
		q.setErr(op, fmt.Errorf("driver %q does not support vector search", q.driver.DriverName()))
		return nil, nil, false
	}
	return grammar, vg, true
}

// OrderByDistance orders the result set by the vector distance between column
// and vec under metric, nearest first. It is the building block for k-nearest-
// neighbour search:
//
//	docs, err := Model[Document]{}.
//	    OrderByDistance("embedding", queryVec, orm.DistanceCosine).
//	    Limit(10).
//	    Get(ctx)
//
// The distance is evaluated in ORDER BY only; it is not added to the projection
// (use SelectDistance to also return the score). The query vector is bound as a
// parameter, never interpolated. Postgres-only: on a driver without the
// VectorGrammar capability the query fails with a clear error from the next
// terminal method.
func (q *Query[T]) OrderByDistance(column string, vec Vector, metric DistanceMetric) *Query[T] {
	if q.err != nil {
		return q
	}
	if err := validateIdentifier(column); err != nil {
		q.setErr("OrderByDistance", err)
		return q
	}
	grammar, vg, ok := q.vectorGrammar("OrderByDistance")
	if !ok {
		return q
	}
	expr, err := vg.VectorDistanceExpr(grammar.QuoteIdentifier(column), string(metric))
	if err != nil {
		q.setErr("OrderByDistance", err)
		return q
	}
	q.orders = append(q.orders, drivers.Order{
		Expr:      expr,
		Args:      []any{vec},
		Direction: "ASC",
	})
	return q
}

// NearestNeighbors is sugar for OrderByDistance(...).Limit(k): the k rows whose
// column vector is closest to vec under metric, nearest first.
func (q *Query[T]) NearestNeighbors(column string, vec Vector, metric DistanceMetric, k int) *Query[T] {
	return q.OrderByDistance(column, vec, metric).Limit(k)
}

// SelectDistance adds the vector distance between column and vec under metric to
// the projection as alias, so the computed score can be scanned into a model
// field whose column matches alias (e.g. a `Distance float64` field for
// alias "distance"). It does not order the results; combine with
// OrderByDistance for ranked search with a returned score.
//
// When no explicit Select has been set, the model's columns are still selected
// (via "*") alongside the distance, so adding a score never silently drops the
// row's own columns.
func (q *Query[T]) SelectDistance(column string, vec Vector, metric DistanceMetric, alias string) *Query[T] {
	if q.err != nil {
		return q
	}
	if err := validateIdentifier(column); err != nil {
		q.setErr("SelectDistance", err)
		return q
	}
	if err := validateIdentifier(alias); err != nil {
		q.setErr("SelectDistance", err)
		return q
	}
	grammar, vg, ok := q.vectorGrammar("SelectDistance")
	if !ok {
		return q
	}
	expr, err := vg.VectorDistanceExpr(grammar.QuoteIdentifier(column), string(metric))
	if err != nil {
		q.setErr("SelectDistance", err)
		return q
	}
	if len(q.columns) == 0 {
		q.columns = []string{"*"}
	}
	q.rawColumns = append(q.rawColumns, drivers.RawColumn{
		Expr: expr + " AS " + grammar.QuoteIdentifier(alias),
		Args: []any{vec},
	})
	return q
}
