package orm

import (
	"context"

	"github.com/velocitykode/velocity/orm/drivers"
)

// PaginatedResult holds a page of query results along with pagination metadata.
// It implements the resource.Paginator interface for seamless integration with
// resource.NewPaginatedCollection and resource.FromPaginator.
type PaginatedResult[T any] struct {
	items       []T
	total       int
	perPage     int
	currentPage int
	lastPage    int
}

// Data returns the paginated items.
func (p *PaginatedResult[T]) Data() []T {
	return p.items
}

// Total returns the total number of matching records (across all pages).
func (p *PaginatedResult[T]) Total() int {
	return p.total
}

// PerPage returns the number of items per page.
func (p *PaginatedResult[T]) PerPage() int {
	return p.perPage
}

// CurrentPage returns the current page number.
func (p *PaginatedResult[T]) CurrentPage() int {
	return p.currentPage
}

// LastPage returns the last page number.
func (p *PaginatedResult[T]) LastPage() int {
	return p.lastPage
}

// Items returns the paginated items as any (implements resource.Paginator).
func (p *PaginatedResult[T]) Items() any {
	return p.items
}

// Paginate executes the query with pagination, returning a PaginatedResult
// containing the items for the requested page and pagination metadata.
// Page numbers start at 1. If page < 1, it defaults to 1.
// If perPage < 1, it defaults to 15. Takes ctx as the first argument
// so reads participate in the caller's transaction when ctx carries a
// *sql.Tx.
func (q *Query[T]) Paginate(ctx context.Context, page, perPage int) (*PaginatedResult[T], error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 15
	}
	if q.err != nil {
		return nil, q.err
	}
	if err := q.bindTxFromContextValue(ctx); err != nil {
		return nil, err
	}

	// Apply global scopes once on q so the count and data queries use
	// identical conditions. The countQ inherits globalScopesApplied=true
	// so Count() does not re-apply (which would duplicate predicates).
	// This is safe because Paginate is a terminal method, the query is
	// not reused afterward.
	q.applyGlobalScopes(ctx)
	// A scope predicate that fails validation sets q.err during apply.
	// Surface it before issuing the count SELECT so a broken scope cannot
	// silently drop its predicate.
	if q.err != nil {
		return nil, q.err
	}

	// Copy conditions for the count query so Count()'s column mutation
	// does not affect the data query.
	countConditions := make([]drivers.Condition, len(q.conditions))
	copy(countConditions, q.conditions)

	countQ := &Query[T]{
		driver:              q.driver,
		mgr:                 q.mgr,
		table:               q.table,
		conditions:          countConditions,
		joins:               q.joins,
		groups:              q.groups,
		having:              q.having,
		distinct:            q.distinct,
		columns:             q.columns,
		rawColumns:          q.rawColumns,
		globalScopesApplied: true,
	}

	total, err := countQ.Count(ctx)
	if err != nil {
		return nil, err
	}

	lastPage := 0
	if perPage > 0 {
		lastPage = (total + perPage - 1) / perPage
	}

	// Fetch the page.
	offset := (page - 1) * perPage
	q.Limit(perPage).Offset(offset)

	items, err := q.Get(ctx)
	if err != nil {
		return nil, err
	}

	return &PaginatedResult[T]{
		items:       items,
		total:       total,
		perPage:     perPage,
		currentPage: page,
		lastPage:    lastPage,
	}, nil
}
