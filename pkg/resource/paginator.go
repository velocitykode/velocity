package resource

// Paginator is an interface that any pagination result can implement.
// This allows the resource package to build PaginationMeta from ORM
// paginators without importing pkg/orm.
type Paginator interface {
	Total() int
	PerPage() int
	CurrentPage() int
	Items() any
}

// FromPaginator builds a PaginationMeta from any Paginator implementation.
// LastPage is auto-computed via ceiling division.
func FromPaginator(p Paginator) PaginationMeta {
	total := p.Total()
	perPage := p.PerPage()
	lastPage := 0
	if perPage > 0 {
		lastPage = (total + perPage - 1) / perPage
	}
	return PaginationMeta{
		Total:       total,
		PerPage:     perPage,
		CurrentPage: p.CurrentPage(),
		LastPage:    lastPage,
	}
}
