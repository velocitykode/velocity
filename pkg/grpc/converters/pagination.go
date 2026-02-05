package converters

import (
	"math"
)

// DefaultPageSize is the default number of items per page
const DefaultPageSize = 20

// MaxPageSize is the maximum allowed page size
const MaxPageSize = 100

// PaginationRequest represents a pagination request from gRPC
type PaginationRequest struct {
	Page     int32
	PageSize int32
}

// PaginationResponse represents pagination metadata for a response
type PaginationResponse struct {
	Page       int32
	PageSize   int32
	TotalItems int32
	TotalPages int32
	HasNext    bool
	HasPrev    bool
}

// Pagination holds the calculated offset and limit for database queries
type Pagination struct {
	Offset int
	Limit  int
	Page   int
	Size   int
}

// NormalizePagination converts page/pageSize to offset/limit with validation.
// Ensures page >= 1 and pageSize is within reasonable bounds.
func NormalizePagination(page, pageSize int32) Pagination {
	// Default and validate page
	if page < 1 {
		page = 1
	}

	// Default and validate page size
	if pageSize < 1 {
		pageSize = DefaultPageSize
	}
	if pageSize > MaxPageSize {
		pageSize = MaxPageSize
	}

	offset := (int(page) - 1) * int(pageSize)

	return Pagination{
		Offset: offset,
		Limit:  int(pageSize),
		Page:   int(page),
		Size:   int(pageSize),
	}
}

// NewPaginationResponse creates a pagination response from total count.
func NewPaginationResponse(page, pageSize, totalItems int32) PaginationResponse {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = DefaultPageSize
	}

	totalPages := int32(math.Ceil(float64(totalItems) / float64(pageSize)))
	if totalPages < 1 {
		totalPages = 1
	}

	return PaginationResponse{
		Page:       page,
		PageSize:   pageSize,
		TotalItems: totalItems,
		TotalPages: totalPages,
		HasNext:    page < totalPages,
		HasPrev:    page > 1,
	}
}

// CalculateTotalPages calculates the total number of pages.
func CalculateTotalPages(totalItems, pageSize int32) int32 {
	if pageSize <= 0 {
		pageSize = DefaultPageSize
	}
	pages := int32(math.Ceil(float64(totalItems) / float64(pageSize)))
	if pages < 1 {
		return 1
	}
	return pages
}

// CursorPagination represents cursor-based pagination.
type CursorPagination struct {
	Cursor string
	Limit  int
}

// CursorResponse represents cursor-based pagination response metadata.
type CursorResponse struct {
	NextCursor string
	PrevCursor string
	HasMore    bool
	Limit      int
}

// NormalizeCursorPagination validates cursor pagination parameters.
func NormalizeCursorPagination(cursor string, limit int32) CursorPagination {
	if limit < 1 {
		limit = int32(DefaultPageSize)
	}
	if limit > int32(MaxPageSize) {
		limit = int32(MaxPageSize)
	}

	return CursorPagination{
		Cursor: cursor,
		Limit:  int(limit),
	}
}

// NewCursorResponse creates a cursor response.
func NewCursorResponse(nextCursor, prevCursor string, hasMore bool, limit int) CursorResponse {
	return CursorResponse{
		NextCursor: nextCursor,
		PrevCursor: prevCursor,
		HasMore:    hasMore,
		Limit:      limit,
	}
}
