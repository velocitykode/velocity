package contract

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/velocitykode/velocity/internal/errchain"
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
		// notParity marks a chain whose answer differs from errors.As on
		// purpose: the walk is breadth first and bounded.
		notParity bool
	}{
		{"plain", errors.New("boom"), 500, "", false},
		{"direct", node(404, "a"), 404, "a", false},
		{"wrapped", fmt.Errorf("ctx: %w", node(403, "a")), 403, "a", false},
		{"double wrapped", fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", node(409, "a"))), 409, "a", false},
		{"typed nil", fmt.Errorf("ctx: %w", (*HTTPError)(nil)), 500, "", false},
		{"status and header on different nodes", &statusOnlyError{status: 422, inner: &headerOnlyError{header: http.Header{"X-Node": {"b"}}}}, 422, "b", false},
		{"joined first wins", errors.Join(errors.New("x"), node(404, "a"), node(409, "b")), 404, "a", false},
		{"joined split targets", errors.Join(&statusOnlyError{status: 418}, &headerOnlyError{header: http.Header{"X-Node": {"b"}}}), 418, "b", false},
		{"joined nested breadth first", errors.Join(fmt.Errorf("w: %w", errors.Join(errors.New("x"), node(401, "a"))), node(402, "b")), 402, "b", true},
		{"multi %w", fmt.Errorf("%w and %w", errors.New("x"), node(410, "a")), 410, "a", false},
		{"multi with nil entry", multiError{nil, node(411, "a")}, 411, "a", false},
		{"as method answers status", &asMethodError{status: node(418, "as"), inner: node(429, "inner")}, 418, "inner", false},
		{"as method declines, child answers", &asMethodError{inner: node(429, "inner")}, 429, "inner", false},
		{"as method inside join after header sibling", errors.Join(&headerOnlyError{header: http.Header{"X-Node": {"b"}}}, &asMethodError{status: node(451, "as")}), 451, "b", false},
		{"wrapped as method", fmt.Errorf("w: %w", &asMethodError{status: &statusOnlyError{status: 423}}), 423, "", false},
		{"at the walk bound", deepWrap(node(503, "deep"), errchain.Max-1), 503, "deep", false},
		{"past the walk bound", deepWrap(node(503, "deep"), errchain.Max), 500, "", true},
		{"joined at the walk bound", deepWrap(errors.Join(errors.New("x"), node(502, "deep")), errchain.Max-3), 502, "deep", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, headers, ok := StatusOf(tt.err)
			refStatus, refHeaders, refOK := statusOfReference(tt.err)
			if !tt.notParity && (status != refStatus || ok != refOK) {
				t.Errorf("StatusOf() = (%d, %v), errors.As gives (%d, %v)", status, ok, refStatus, refOK)
			}
			if got, ref := headers.Get("X-Node"), refHeaders.Get("X-Node"); !tt.notParity && got != ref {
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

// BenchmarkStatusOf holds StatusOf to no allocation per call; the CI
// zero-allocation check runs it.
func BenchmarkStatusOf(b *testing.B) {
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
		b.Run(tt.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, _, _ = StatusOf(tt.err)
			}
		})
	}
}
