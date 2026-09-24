package problem

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
)

var updateGolden = flag.Bool("update", false, "rewrite the problem+json golden files")

// goldenStack is a fixed stack trace so the debug golden is stable.
var goldenStack = &contract.StackTrace{Frames: []contract.StackFrame{
	{File: "/app/handlers/orders.go", Line: 42, Function: "Show", Package: "example.test/app/handlers"},
}}

func TestJSONRenderer_Golden(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		debug      bool
		ctx        *ErrorContext
		path       string
		wantStatus int
		wantHeader map[string]string
	}{
		{
			name:       "not_found",
			err:        &contract.HTTPError{Status: http.StatusNotFound, Message: "Order 7 not found"},
			ctx:        &ErrorContext{RequestID: "req-404", TraceID: "trace-404"},
			path:       "/orders/7",
			wantStatus: http.StatusNotFound,
		},
		{
			name: "unprocessable_with_errors",
			err: &fieldsErr{fields: map[string][]string{
				"email": {"The email has already been taken."},
				"name":  {"The name field is required.", "The name must be a string."},
			}},
			path:       "/users",
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name:       "too_many_requests",
			err:        (&contract.HTTPError{Status: http.StatusTooManyRequests, Message: "Slow down"}).WithHeader("Retry-After", "30"),
			path:       "/login",
			wantStatus: http.StatusTooManyRequests,
			wantHeader: map[string]string{"Retry-After": "30"},
		},
		{
			name:       "internal_production",
			err:        &contract.HTTPError{Status: http.StatusInternalServerError, Message: "db password hunter2 rejected", Cause: errors.New("dial tcp 10.0.0.5:5432: refused")},
			ctx:        &ErrorContext{RequestID: "req-500", StackTrace: goldenStack, Extra: map[string]any{"tenant": "acme"}},
			path:       "/orders",
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "plain_error_production",
			err:        errors.New("sql: no rows in result set for secret table"),
			path:       "/orders",
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:  "internal_debug",
			err:   &contract.HTTPError{Status: http.StatusInternalServerError, Message: "db password hunter2 rejected", Cause: errors.New("dial tcp 10.0.0.5:5432: refused")},
			debug: true,
			ctx: &ErrorContext{
				RequestID:  "req-500",
				TraceID:    "trace-500",
				StackTrace: goldenStack,
				Extra:      map[string]any{"tenant": "acme", "unencodable": math.Inf(1)},
			},
			path:       "/orders",
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "typed_conflict",
			err:        typedErr{},
			path:       "/orders/7",
			wantStatus: http.StatusConflict,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, _ := newTestHandler(WithDebug(tt.debug), WithAPIMode(true))
			rc, w := newRC(http.MethodGet, tt.path)
			ctx := tt.ctx
			if ctx == nil {
				ctx = &ErrorContext{}
			}
			ctx.Timestamp = time.Unix(0, 0)
			h.HandleRequest(rc, tt.err, ctx)

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if got := w.Header().Get("Content-Type"); got != ProblemTypeContent {
				t.Errorf("Content-Type = %q, want %q", got, ProblemTypeContent)
			}
			if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
			}
			for k, v := range tt.wantHeader {
				if got := w.Header().Get(k); got != v {
					t.Errorf("%s = %q, want %q", k, got, v)
				}
			}

			var pretty bytes.Buffer
			if err := json.Indent(&pretty, w.Body.Bytes(), "", "  "); err != nil {
				t.Fatalf("body is not JSON: %v: %s", err, w.Body.String())
			}
			pretty.WriteByte('\n')
			path := filepath.Join("testdata", tt.name+".golden.json")
			if *updateGolden {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, pretty.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden (run with -update to create): %v", err)
			}
			if !bytes.Equal(pretty.Bytes(), want) {
				t.Errorf("body mismatch for %s\n got: %s\nwant: %s", path, pretty.String(), want)
			}
		})
	}
}

func TestJSONRenderer_ServerErrorsNeverEchoOutsideDebug(t *testing.T) {
	secrets := []string{"hunter2", "10.0.0.5", "secret table", "exception", "stack_trace", "origin", "previous", "context"}
	tests := []struct {
		name string
		err  error
	}{
		{"InternalMessage", Internal("hunter2")},
		{"InternalCause", Internal().WithCause(errors.New("dial 10.0.0.5"))},
		{"ServiceUnavailableMessage", ServiceUnavailable(0, "hunter2 maintenance")},
		{"PlainError", errors.New("secret table")},
		{"WrappedClientErrorInsideServer", (&contract.HTTPError{Status: 502, Message: "hunter2"}).WithCause(NotFound("secret table"))},
		{"FieldErrorsOn5xxHidden", &contract.HTTPError{Status: 500, Cause: &fieldsErr{fields: map[string][]string{"hunter2": {"x"}}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, _ := newTestHandler(WithAPIMode(true))
			rc, w := newRC(http.MethodGet, "/x")
			h.HandleRequest(rc, tt.err, &ErrorContext{StackTrace: goldenStack, Extra: map[string]any{"k": "v"}})
			body := w.Body.String()
			for _, s := range secrets {
				if strings.Contains(body, s) {
					t.Errorf("body leaks %q: %s", s, body)
				}
			}
		})
	}
}

func TestJSONRenderer_ClientMessageNeedsMatchingStatus(t *testing.T) {
	// A 401 wrapping a 404 HTTPError must not echo the 404's message.
	err := &statusErr{code: http.StatusUnauthorized}
	wrapped := errors.Join(err, NotFound("internal lookup detail"))
	h, _, _ := newTestHandler(WithAPIMode(true))
	rc, w := newRC(http.MethodGet, "/x")
	h.HandleRequest(rc, wrapped, nil)
	if strings.Contains(w.Body.String(), "internal lookup detail") {
		t.Errorf("mismatched-status message echoed: %s", w.Body.String())
	}
}

func TestJSONRenderer_OriginInDebug(t *testing.T) {
	h, _, _ := newTestHandler(WithAPIMode(true), WithDebug(true))
	rc, w := newRC(http.MethodGet, "/x")
	h.HandleRequest(rc, Conflict("taken"), nil)
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	origin, _ := body["origin"].(string)
	if !strings.Contains(origin, "golden_test.go") {
		t.Errorf("origin = %q, want the constructing test file", origin)
	}
	if body["exception"] != "*contract.HTTPError" {
		t.Errorf("exception = %v", body["exception"])
	}
}
