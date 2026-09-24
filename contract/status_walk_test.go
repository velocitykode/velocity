package contract

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

// headerOnlyError carries headers but names no status, optionally wrapping
// another error.
type headerOnlyError struct {
	header http.Header
	inner  error
}

func (e *headerOnlyError) Error() string        { return "header only" }
func (e *headerOnlyError) Headers() http.Header { return e.header }
func (e *headerOnlyError) Unwrap() error        { return e.inner }

// asMethodError answers a *StatusError target through its As method with
// status (when set), and unwraps to inner.
type asMethodError struct {
	status StatusError
	inner  error
}

func (e *asMethodError) Error() string { return "as method" }
func (e *asMethodError) Unwrap() error { return e.inner }
func (e *asMethodError) As(target any) bool {
	if p, ok := target.(*StatusError); ok && e.status != nil {
		*p = e.status
		return true
	}
	return false
}

// multiError unwraps to its elements, nil entries included.
type multiError []error

func (m multiError) Error() string   { return "multi" }
func (m multiError) Unwrap() []error { return m }

// statusOfReference is StatusOf written with errors.As, the behaviour the
// hand walk must match.
func statusOfReference(err error) (int, http.Header, bool) {
	if err == nil {
		return 0, nil, false
	}
	status, ok := http.StatusInternalServerError, false
	var se StatusError
	if errors.As(err, &se) {
		status, ok = validStatus(se.StatusCode()), true
	}
	var headers http.Header
	var he HeaderError
	if errors.As(err, &he) {
		headers = he.Headers()
	}
	return status, headers, ok
}

// node returns an HTTPError at status tagged with an X-Node header, so a
// test can tell which node's headers were chosen.
func node(status int, tag string) *HTTPError {
	return NewHTTPError(status).WithHeader("X-Node", tag)
}

func deepWrap(err error, n int) error {
	for i := 0; i < n; i++ {
		err = fmt.Errorf("layer %d: %w", i, err)
	}
	return err
}

func TestStatusOf_ParityWithErrorsAs(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantNode   string
	}{
		{"plain", errors.New("boom"), 500, ""},
		{"direct", node(404, "a"), 404, "a"},
		{"wrapped", fmt.Errorf("ctx: %w", node(403, "a")), 403, "a"},
		{"double wrapped", fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", node(409, "a"))), 409, "a"},
		{"typed nil", fmt.Errorf("ctx: %w", (*HTTPError)(nil)), 500, ""},
		{"status and header on different nodes", &statusOnlyError{status: 422, inner: &headerOnlyError{header: http.Header{"X-Node": {"b"}}}}, 422, "b"},
		{"joined first wins", errors.Join(errors.New("x"), node(404, "a"), node(409, "b")), 404, "a"},
		{"joined split targets", errors.Join(&statusOnlyError{status: 418}, &headerOnlyError{header: http.Header{"X-Node": {"b"}}}), 418, "b"},
		{"joined nested depth first", errors.Join(fmt.Errorf("w: %w", errors.Join(errors.New("x"), node(401, "a"))), node(402, "b")), 401, "a"},
		{"multi %w", fmt.Errorf("%w and %w", errors.New("x"), node(410, "a")), 410, "a"},
		{"multi with nil entry", multiError{nil, node(411, "a")}, 411, "a"},
		{"as method answers status", &asMethodError{status: node(418, "as"), inner: node(429, "inner")}, 418, "inner"},
		{"as method declines, child answers", &asMethodError{inner: node(429, "inner")}, 429, "inner"},
		{"as method inside join after header sibling", errors.Join(&headerOnlyError{header: http.Header{"X-Node": {"b"}}}, &asMethodError{status: node(451, "as")}), 451, "b"},
		{"wrapped as method", fmt.Errorf("w: %w", &asMethodError{status: &statusOnlyError{status: 423}}), 423, ""},
		{"beyond walk limit", deepWrap(node(503, "deep"), chainWalkLimit+10), 503, "deep"},
		{"joined beyond walk limit", deepWrap(errors.Join(errors.New("x"), node(502, "deep")), chainWalkLimit+1), 502, "deep"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, headers, ok := StatusOf(tt.err)
			refStatus, refHeaders, refOK := statusOfReference(tt.err)
			if status != refStatus || ok != refOK {
				t.Errorf("StatusOf() = (%d, %v), errors.As gives (%d, %v)", status, ok, refStatus, refOK)
			}
			if got, ref := headers.Get("X-Node"), refHeaders.Get("X-Node"); got != ref {
				t.Errorf("X-Node = %q, errors.As gives %q", got, ref)
			}
			if status != tt.wantStatus {
				t.Errorf("status = %d, want %d", status, tt.wantStatus)
			}
			if got := headers.Get("X-Node"); got != tt.wantNode {
				t.Errorf("X-Node = %q, want %q", got, tt.wantNode)
			}
		})
	}
}

func TestStatusOf_Allocations(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"direct", NewHTTPError(http.StatusNotFound)},
		{"direct with header", NewHTTPError(http.StatusMethodNotAllowed).WithHeader("Allow", "GET")},
		{"wrapped", fmt.Errorf("ctx: %w", NewHTTPError(http.StatusForbidden))},
		{"joined", errors.Join(errors.New("x"), NewHTTPError(http.StatusConflict))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allocs := testing.AllocsPerRun(100, func() {
				_, _, _ = StatusOf(tt.err)
			})
			if allocs != 0 {
				t.Errorf("StatusOf allocated %.0f times per call, want 0", allocs)
			}
		})
	}
}
