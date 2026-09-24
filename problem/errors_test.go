package problem

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestConstructors_StatusMessageOrigin(t *testing.T) {
	tests := []struct {
		name    string
		err     *HTTPError
		status  int
		message string
	}{
		{"BadRequest", BadRequest(), http.StatusBadRequest, "Bad Request"},
		{"BadRequest_Message", BadRequest("bad input"), http.StatusBadRequest, "bad input"},
		{"Unauthorized", Unauthorized(), http.StatusUnauthorized, "Unauthorized"},
		{"Forbidden", Forbidden("nope"), http.StatusForbidden, "nope"},
		{"NotFound", NotFound(), http.StatusNotFound, "Not Found"},
		{"MethodNotAllowed", MethodNotAllowed("GET"), http.StatusMethodNotAllowed, "Method Not Allowed"},
		{"Conflict", Conflict(), http.StatusConflict, "Conflict"},
		{"Gone", Gone(), http.StatusGone, "Gone"},
		{"PayloadTooLarge", PayloadTooLarge(), http.StatusRequestEntityTooLarge, "Request Entity Too Large"},
		{"TokenMismatch", TokenMismatch(), StatusTokenMismatch, "CSRF token mismatch"},
		{"TokenMismatch_Message", TokenMismatch("expired"), StatusTokenMismatch, "expired"},
		{"TooManyRequests", TooManyRequests(0), http.StatusTooManyRequests, "Too Many Requests"},
		{"Internal", Internal("db down"), http.StatusInternalServerError, "db down"},
		{"ServiceUnavailable", ServiceUnavailable(0), http.StatusServiceUnavailable, "Service Unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.StatusCode(); got != tt.status {
				t.Errorf("StatusCode() = %d, want %d", got, tt.status)
			}
			if tt.err.Message != tt.message {
				t.Errorf("Message = %q, want %q", tt.err.Message, tt.message)
			}
			if origin := tt.err.Origin(); !strings.Contains(origin, "errors_test.go") {
				t.Errorf("Origin() = %q, want the calling test file", origin)
			}
		})
	}
}

func TestConstructors_Headers(t *testing.T) {
	tests := []struct {
		name   string
		err    *HTTPError
		header string
		want   string
	}{
		{"MethodNotAllowed_Allow", MethodNotAllowed("GET", "POST"), "Allow", "GET, POST"},
		{"MethodNotAllowed_NoMethods", MethodNotAllowed(), "Allow", ""},
		{"MethodNotAllowed_CRLFDropped", MethodNotAllowed("GET\r\nX-Evil: 1"), "Allow", ""},
		{"TooManyRequests_WholeSeconds", TooManyRequests(30 * time.Second), "Retry-After", "30"},
		{"TooManyRequests_RoundsUp", TooManyRequests(1500 * time.Millisecond), "Retry-After", "2"},
		{"TooManyRequests_SubSecond", TooManyRequests(10 * time.Millisecond), "Retry-After", "1"},
		{"TooManyRequests_Zero", TooManyRequests(0), "Retry-After", ""},
		{"TooManyRequests_Negative", TooManyRequests(-time.Second), "Retry-After", ""},
		{"ServiceUnavailable_RetryAfter", ServiceUnavailable(time.Minute), "Retry-After", "60"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Headers().Get(tt.header); got != tt.want {
				t.Errorf("%s = %q, want %q", tt.header, got, tt.want)
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
		{419, "Page Expired"},
		{599, "Error"},
		{500, "Internal Server Error"},
	}
	for _, tt := range tests {
		if got := statusTitle(tt.status); got != tt.want {
			t.Errorf("statusTitle(%d) = %q, want %q", tt.status, got, tt.want)
		}
	}
}
