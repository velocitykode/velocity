package contract

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// embeddingError embeds *HTTPError the way a subsystem error might; the
// promoted methods make it a StatusError and HeaderError itself.
type embeddingError struct {
	*HTTPError
	Detail string
}

// statusOnlyError names a status but carries no headers, optionally
// wrapping another error.
type statusOnlyError struct {
	status int
	inner  error
}

func (e *statusOnlyError) Error() string   { return "status only" }
func (e *statusOnlyError) StatusCode() int { return e.status }
func (e *statusOnlyError) Unwrap() error   { return e.inner }

func TestNewHTTPError_Fields(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		message     []string
		wantMessage string
		wantStatus  int
		wantReport  bool
	}{
		{"default message", http.StatusNotFound, nil, "Not Found", 404, false},
		{"custom message", http.StatusForbidden, []string{"No access"}, "No access", 403, false},
		{"empty message falls back", http.StatusBadRequest, []string{""}, "Bad Request", 400, false},
		{"extra messages ignored", http.StatusConflict, []string{"first", "second"}, "first", 409, false},
		{"server error reports", http.StatusInternalServerError, nil, "Internal Server Error", 500, true},
		{"503 reports", http.StatusServiceUnavailable, nil, "Service Unavailable", 503, true},
		{"499 unnamed status", 499, nil, "", 499, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := NewHTTPError(tt.status, tt.message...)
			if e.Message != tt.wantMessage {
				t.Errorf("Message = %q, want %q", e.Message, tt.wantMessage)
			}
			if got := e.StatusCode(); got != tt.wantStatus {
				t.Errorf("StatusCode() = %d, want %d", got, tt.wantStatus)
			}
			if got := e.ShouldReport(); got != tt.wantReport {
				t.Errorf("ShouldReport() = %v, want %v", got, tt.wantReport)
			}
		})
	}
}

func TestHTTPError_Error(t *testing.T) {
	cause := errors.New("db down")
	tests := []struct {
		name string
		err  *HTTPError
		want string
	}{
		{"message only", &HTTPError{Status: 404, Message: "Missing"}, "Missing"},
		{"empty message uses status text", &HTTPError{Status: 404}, "Not Found"},
		{"unnamed status", &HTTPError{Status: 499}, "status 499"},
		{"invalid status", &HTTPError{Status: 0}, "Internal Server Error"},
		{"cause appended", &HTTPError{Status: 500, Message: "Boom", Cause: cause}, "Boom: db down"},
		{"nil receiver", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHTTPError_StatusCode_Invalid(t *testing.T) {
	tests := []struct {
		name string
		err  *HTTPError
		want int
	}{
		{"zero", &HTTPError{}, 500},
		{"below range", &HTTPError{Status: 99}, 500},
		{"above range", &HTTPError{Status: 1000}, 500},
		{"lower bound", &HTTPError{Status: 100}, 100},
		{"upper bound", &HTTPError{Status: 999}, 999},
		{"nil receiver", nil, 500},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.StatusCode(); got != tt.want {
				t.Errorf("StatusCode() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestHTTPError_NilReceiver(t *testing.T) {
	var e *HTTPError
	if e.Headers() != nil {
		t.Error("Headers() on nil should be nil")
	}
	if e.Unwrap() != nil {
		t.Error("Unwrap() on nil should be nil")
	}
	if e.WithHeader("X", "y") != nil {
		t.Error("WithHeader on nil should return nil")
	}
	if e.WithCause(errors.New("x")) != nil {
		t.Error("WithCause on nil should return nil")
	}
	if e.WithOrigin(0) != nil {
		t.Error("WithOrigin on nil should return nil")
	}
	if e.Origin() != "" {
		t.Error("Origin on nil should be empty")
	}
	if !e.ShouldReport() {
		t.Error("ShouldReport on nil should follow the 500 fallback")
	}
}

func TestHTTPError_WithHeader(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   string
		wantSet bool
	}{
		{"valid", "Retry-After", "30", true},
		{"canonicalised key", "retry-after", "30", true},
		{"CR in value", "Retry-After", "30\r", false},
		{"LF in value", "Retry-After", "30\nSet-Cookie: x=y", false},
		{"CRLF in key", "X-Evil\r\nSet-Cookie", "v", false},
		{"LF in key", "X-Evil\n", "v", false},
		{"empty key", "", "v", false},
		{"empty value allowed", "X-Empty", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := NewHTTPError(http.StatusTooManyRequests)
			got := e.WithHeader(tt.key, tt.value)
			if got != e {
				t.Fatal("WithHeader must return the same pointer")
			}
			_, present := e.Headers()[http.CanonicalHeaderKey(tt.key)]
			if present != tt.wantSet {
				t.Errorf("header present = %v, want %v (headers %v)", present, tt.wantSet, e.Headers())
			}
			if !tt.wantSet && len(e.Headers()) != 0 {
				t.Errorf("rejected header left entries: %v", e.Headers())
			}
		})
	}
}

func TestHTTPError_WithHeader_Chain(t *testing.T) {
	e := NewHTTPError(http.StatusMethodNotAllowed).
		WithHeader("Allow", "GET, POST").
		WithHeader("X-Bad", "a\r\nb").
		WithHeader("Cache-Control", "no-store")
	if got := e.Headers().Get("Allow"); got != "GET, POST" {
		t.Errorf("Allow = %q", got)
	}
	if got := e.Headers().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	if _, ok := e.Headers()["X-Bad"]; ok {
		t.Error("CR/LF header must be dropped")
	}
}

func TestHTTPError_WithCause(t *testing.T) {
	sentinel := errors.New("not found in store")
	e := NewHTTPError(http.StatusNotFound)
	if got := e.WithCause(sentinel); got != e {
		t.Fatal("WithCause must return the same pointer")
	}
	if !errors.Is(e, sentinel) {
		t.Error("errors.Is must reach the cause")
	}
	if errors.Unwrap(e) != sentinel {
		t.Error("Unwrap must return the cause")
	}
	if e.Message != "Not Found" {
		t.Errorf("Message changed to %q", e.Message)
	}
}

func originHelper() *HTTPError {
	return (&HTTPError{Status: http.StatusNotFound}).WithOrigin(1)
}

func TestHTTPError_Origin(t *testing.T) {
	tests := []struct {
		name     string
		build    func() *HTTPError
		wantFunc string
	}{
		{"NewHTTPError records caller", func() *HTTPError { return NewHTTPError(http.StatusNotFound) }, "TestHTTPError_Origin.func1"},
		{"WithOrigin(0) records caller", func() *HTTPError { return (&HTTPError{Status: 404}).WithOrigin(0) }, "TestHTTPError_Origin.func2"},
		{"WithOrigin(1) skips wrapper", func() *HTTPError { return originHelper() }, "TestHTTPError_Origin.func3"},
		{"negative skip clamps to caller", func() *HTTPError { return (&HTTPError{Status: 404}).WithOrigin(-5) }, "TestHTTPError_Origin.func4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origin := tt.build().Origin()
			if !strings.Contains(origin, "http_error_test.go:") {
				t.Errorf("Origin() = %q, want this test file", origin)
			}
			if !strings.HasSuffix(origin, tt.wantFunc) {
				t.Errorf("Origin() = %q, want function %s", origin, tt.wantFunc)
			}
		})
	}
}

func TestHTTPError_Origin_Literal(t *testing.T) {
	e := &HTTPError{Status: http.StatusNotFound}
	if got := e.Origin(); got != "" {
		t.Errorf("literal Origin() = %q, want empty", got)
	}
}

func TestErrorsAs_StatusError(t *testing.T) {
	direct := NewHTTPError(http.StatusNotFound)
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"direct", direct, 404},
		{"wrapped", fmt.Errorf("load user: %w", direct), 404},
		{"double wrapped", fmt.Errorf("a: %w", fmt.Errorf("b: %w", NewHTTPError(http.StatusGone))), 410},
		{"embedding value", &embeddingError{HTTPError: NewHTTPError(http.StatusConflict)}, 409},
		{"wrapped embedding value", fmt.Errorf("x: %w", &embeddingError{HTTPError: NewHTTPError(http.StatusTeapot)}), 418},
		{"joined", errors.Join(errors.New("other"), NewHTTPError(http.StatusBadRequest)), 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var se StatusError
			if !errors.As(tt.err, &se) {
				t.Fatal("errors.As(StatusError) = false")
			}
			if se.StatusCode() != tt.want {
				t.Errorf("StatusCode() = %d, want %d", se.StatusCode(), tt.want)
			}
		})
	}
}

func TestStatusOf(t *testing.T) {
	retry := NewHTTPError(http.StatusTooManyRequests).WithHeader("Retry-After", "30")
	tests := []struct {
		name        string
		err         error
		wantStatus  int
		wantOK      bool
		wantHeader  string // Retry-After value expected, "" for none
		wantHeaders bool
	}{
		{"nil", nil, 0, false, "", false},
		{"plain error", errors.New("boom"), 500, false, "", false},
		{"direct", NewHTTPError(http.StatusNotFound), 404, true, "", false},
		{"wrapped", fmt.Errorf("ctx: %w", NewHTTPError(http.StatusForbidden)), 403, true, "", false},
		{"headers direct", retry, 429, true, "30", true},
		{"headers wrapped", fmt.Errorf("limit: %w", retry), 429, true, "30", true},
		{"embedding keeps headers", &embeddingError{HTTPError: NewHTTPError(http.StatusTooManyRequests).WithHeader("Retry-After", "5")}, 429, true, "5", true},
		{"status only", &statusOnlyError{status: http.StatusUnprocessableEntity}, 422, true, "", false},
		{"status only wrapping header error", &statusOnlyError{status: http.StatusServiceUnavailable, inner: retry}, 503, true, "30", true},
		{"invalid status", &statusOnlyError{status: 42}, 500, true, "", false},
		{"marked reported", MarkReported(NewHTTPError(http.StatusGone)), 410, true, "", false},
		{"handled", Handled(NewHTTPError(http.StatusBadRequest)), 400, true, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, headers, ok := StatusOf(tt.err)
			if status != tt.wantStatus || ok != tt.wantOK {
				t.Errorf("StatusOf() = (%d, _, %v), want (%d, _, %v)", status, ok, tt.wantStatus, tt.wantOK)
			}
			if got := headers.Get("Retry-After"); got != tt.wantHeader {
				t.Errorf("Retry-After = %q, want %q", got, tt.wantHeader)
			}
			if (len(headers) > 0) != tt.wantHeaders {
				t.Errorf("headers = %v, want present=%v", headers, tt.wantHeaders)
			}
		})
	}
}

func TestHandled(t *testing.T) {
	cause := NewHTTPError(http.StatusForbidden)
	tests := []struct {
		name      string
		err       error
		wantCause error
		wantIs    bool
	}{
		{"handled cause", Handled(cause), cause, true},
		{"wrapped handled", fmt.Errorf("mw: %w", Handled(cause)), cause, true},
		{"handled nil is sentinel", Handled(nil), nil, true},
		{"bare sentinel", ErrResponseWritten, nil, true},
		{"wrapped sentinel", fmt.Errorf("x: %w", ErrResponseWritten), nil, true},
		{"unrelated", errors.New("x"), nil, false},
		{"nil", nil, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := errors.Is(tt.err, ErrResponseWritten); got != tt.wantIs {
				t.Errorf("errors.Is(_, ErrResponseWritten) = %v, want %v", got, tt.wantIs)
			}
			if got := HandledCause(tt.err); got != tt.wantCause {
				t.Errorf("HandledCause() = %v, want %v", got, tt.wantCause)
			}
		})
	}
}

func TestHandled_Transparency(t *testing.T) {
	sentinel := errors.New("token missing")
	cause := NewHTTPError(419).WithCause(sentinel)
	h := Handled(cause)
	if !errors.Is(h, sentinel) {
		t.Error("errors.Is must reach the cause chain")
	}
	var he *HTTPError
	if !errors.As(h, &he) || he != cause {
		t.Error("errors.As must reach the cause")
	}
	if errors.Unwrap(h) != cause {
		t.Error("Unwrap must return the cause")
	}
	if want := "velocity: response already written: " + cause.Error(); h.Error() != want {
		t.Errorf("Error() = %q, want %q", h.Error(), want)
	}
	if Handled(nil) != ErrResponseWritten {
		t.Error("Handled(nil) must be the bare sentinel")
	}
}

func TestMarkReported(t *testing.T) {
	sentinel := errors.New("disk full")
	base := NewHTTPError(http.StatusInternalServerError).WithCause(sentinel)
	marked := MarkReported(base)

	tests := []struct {
		name  string
		check func() bool
	}{
		{"unmarked is not reported", func() bool { return !IsReported(base) }},
		{"marked is reported", func() bool { return IsReported(marked) }},
		{"wrapped marked is reported", func() bool { return IsReported(fmt.Errorf("x: %w", marked)) }},
		{"errors.Is transparent", func() bool { return errors.Is(marked, sentinel) }},
		{"errors.Is to base", func() bool { return errors.Is(marked, base) }},
		{"errors.As transparent", func() bool {
			var he *HTTPError
			return errors.As(marked, &he) && he == base
		}},
		{"errors.As interface", func() bool {
			var se StatusError
			return errors.As(marked, &se) && se.StatusCode() == 500
		}},
		{"Unwrap transparent", func() bool { return errors.Unwrap(marked) == base }},
		{"same text", func() bool { return marked.Error() == base.Error() }},
		{"idempotent", func() bool { return MarkReported(marked) == marked }},
		{"nil stays nil", func() bool { return MarkReported(nil) == nil }},
		{"nil not reported", func() bool { return !IsReported(nil) }},
		{"handled marked", func() bool { return IsReported(Handled(marked)) && HandledCause(Handled(marked)) == marked }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.check() {
				t.Error("check failed")
			}
		})
	}
}

func TestStatusTitle(t *testing.T) {
	tests := []struct {
		status int
		want   string
	}{
		{404, "Not Found"},
		{StatusTokenMismatch, "Page Expired"},
		{599, "Error"},
		{500, "Internal Server Error"},
	}
	for _, tt := range tests {
		if got := StatusTitle(tt.status); got != tt.want {
			t.Errorf("StatusTitle(%d) = %q, want %q", tt.status, got, tt.want)
		}
	}
}
