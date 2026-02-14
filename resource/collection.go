package resource

// NewCollection transforms a slice of Resource-implementing items into
// a slice of maps suitable for JSON serialization.
func NewCollection[T Resource](items []T) []map[string]any {
	result := make([]map[string]any, len(items))
	for i, item := range items {
		result[i] = item.ToResource()
	}
	return result
}

// PaginationMeta holds pagination metadata for paginated collections.
type PaginationMeta struct {
	Total       int `json:"total"`
	PerPage     int `json:"per_page"`
	CurrentPage int `json:"current_page"`
	LastPage    int `json:"last_page"`
}

// NewPaginatedCollection transforms a slice of Resource-implementing items
// into a paginated response with data and meta fields.
// LastPage is auto-computed from Total and PerPage (ceiling division).
// Negative Total or PerPage are clamped to 0.
func NewPaginatedCollection[T Resource](items []T, meta PaginationMeta) map[string]any {
	if meta.Total < 0 {
		meta.Total = 0
	}
	if meta.PerPage < 0 {
		meta.PerPage = 0
	}
	if meta.PerPage > 0 {
		meta.LastPage = (meta.Total + meta.PerPage - 1) / meta.PerPage
	}
	return map[string]any{
		"data": NewCollection(items),
		"meta": meta,
	}
}
