package router

import (
	"io"
	"net/http/httptest"
)

// NewTestContext creates a Context and ResponseRecorder suitable for unit
// tests. An optional body reader may be provided for POST/PUT/PATCH tests.
func NewTestContext(method, path string, body ...io.Reader) (*Context, *httptest.ResponseRecorder) {
	var b io.Reader
	if len(body) > 0 {
		b = body[0]
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, path, b)
	return &Context{
		Response: w,
		Request:  r,
		params:   make([]RouteParam, 0, 8),
		values:   make(map[string]interface{}),
	}, w
}
