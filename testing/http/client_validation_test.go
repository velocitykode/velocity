package http

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// validationMockT records Errorf calls so failure paths can be asserted.
type validationMockT struct {
	errors []string
}

func (m *validationMockT) Helper() {}
func (m *validationMockT) Errorf(format string, args ...interface{}) {
	m.errors = append(m.errors, fmt.Sprintf(format, args...))
}

// newResponse builds a TestResponse over a recorder with the given status and
// raw JSON body.
func newResponse(mt *validationMockT, status int, body string) *TestResponse {
	rec := httptest.NewRecorder()
	rec.Code = status
	rec.Body.WriteString(body)
	return &TestResponse{t: mt, recorder: rec}
}

const validationBody = `{"message":"The given data was invalid.","errors":{"email":["The email field is required.","The email must be valid."],"name":["The name field is required."]}}`

func TestAssertInvalid(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		fields    []string
		wantError bool
	}{
		{
			name:      "all fields invalid",
			status:    http.StatusUnprocessableEntity,
			body:      validationBody,
			fields:    []string{"email", "name"},
			wantError: false,
		},
		{
			name:      "single field invalid",
			status:    http.StatusUnprocessableEntity,
			body:      validationBody,
			fields:    []string{"email"},
			wantError: false,
		},
		{
			name:      "missing field reports error",
			status:    http.StatusUnprocessableEntity,
			body:      validationBody,
			fields:    []string{"password"},
			wantError: true,
		},
		{
			name:      "wrong status reports error",
			status:    http.StatusOK,
			body:      `{"errors":{"email":["required"]}}`,
			fields:    []string{"email"},
			wantError: true,
		},
		{
			name:      "no errors object reports error",
			status:    http.StatusUnprocessableEntity,
			body:      `{"message":"nope"}`,
			fields:    []string{"email"},
			wantError: true,
		},
		{
			name:      "flat string field value reports shape error",
			status:    http.StatusUnprocessableEntity,
			body:      `{"errors":{"email":"required"}}`,
			fields:    []string{"email"},
			wantError: true,
		},
		{
			name:      "non-string array element reports shape error",
			status:    http.StatusUnprocessableEntity,
			body:      `{"errors":{"email":[123]}}`,
			fields:    []string{"email"},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mt := &validationMockT{}
			r := newResponse(mt, tt.status, tt.body)
			got := r.AssertInvalid(tt.fields...)
			if got != r {
				t.Error("AssertInvalid did not return the receiver for chaining")
			}
			if hasErr := len(mt.errors) > 0; hasErr != tt.wantError {
				t.Errorf("wantError=%v, got errors=%v", tt.wantError, mt.errors)
			}
		})
	}
}

func TestAssertValidationErrors(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		expected  map[string][]string
		wantError bool
	}{
		{
			name:   "expected messages present",
			status: http.StatusUnprocessableEntity,
			body:   validationBody,
			expected: map[string][]string{
				"email": {"The email field is required."},
				"name":  {"The name field is required."},
			},
			wantError: false,
		},
		{
			name:   "multiple expected for one field",
			status: http.StatusUnprocessableEntity,
			body:   validationBody,
			expected: map[string][]string{
				"email": {"The email field is required.", "The email must be valid."},
			},
			wantError: false,
		},
		{
			name:   "missing message reports error",
			status: http.StatusUnprocessableEntity,
			body:   validationBody,
			expected: map[string][]string{
				"email": {"some other message"},
			},
			wantError: true,
		},
		{
			name:   "missing field reports error",
			status: http.StatusUnprocessableEntity,
			body:   validationBody,
			expected: map[string][]string{
				"password": {"required"},
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mt := &validationMockT{}
			r := newResponse(mt, tt.status, tt.body)
			got := r.AssertValidationErrors(tt.expected)
			if got != r {
				t.Error("AssertValidationErrors did not return the receiver for chaining")
			}
			if hasErr := len(mt.errors) > 0; hasErr != tt.wantError {
				t.Errorf("wantError=%v, got errors=%v", tt.wantError, mt.errors)
			}
		})
	}
}

func TestAssertValid(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		wantError bool
	}{
		{
			name:      "ok with no errors object",
			status:    http.StatusOK,
			body:      `{"data":{"id":1}}`,
			wantError: false,
		},
		{
			name:      "ok with empty errors object",
			status:    http.StatusOK,
			body:      `{"errors":{}}`,
			wantError: false,
		},
		{
			name:      "ok with HTML body",
			status:    http.StatusOK,
			body:      `<html><body>ok</body></html>`,
			wantError: false,
		},
		{
			name:      "ok with empty body",
			status:    http.StatusOK,
			body:      ``,
			wantError: false,
		},
		{
			name:      "ok with JSON array body",
			status:    http.StatusOK,
			body:      `[1,2,3]`,
			wantError: false,
		},
		{
			name:      "422 reports error",
			status:    http.StatusUnprocessableEntity,
			body:      validationBody,
			wantError: true,
		},
		{
			name:      "non-empty errors object reports error",
			status:    http.StatusOK,
			body:      `{"errors":{"email":["required"]}}`,
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mt := &validationMockT{}
			r := newResponse(mt, tt.status, tt.body)
			got := r.AssertValid()
			if got != r {
				t.Error("AssertValid did not return the receiver for chaining")
			}
			if hasErr := len(mt.errors) > 0; hasErr != tt.wantError {
				t.Errorf("wantError=%v, got errors=%v", tt.wantError, mt.errors)
			}
		})
	}
}
