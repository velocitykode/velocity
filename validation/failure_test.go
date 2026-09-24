package validation

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// failedResult returns a Result with errors on email and name.
func failedResult(t *testing.T) *Result {
	t.Helper()
	result, err := CheckData(map[string]interface{}{"email": "nope"}, Rules{
		"email": {Required(), Email()},
		"name":  {Required()},
	})
	if err != nil {
		t.Fatalf("CheckData: %v", err)
	}
	if !result.HasErrors() {
		t.Fatal("expected a failed result")
	}
	return result
}

// Compile-time proof that *Failure is the typed error the pipeline reads.
var (
	_ contract.StatusError = (*Failure)(nil)
	_ contract.Reportable  = (*Failure)(nil)
	_ interface {
		Errors() map[string][]string
	} = (*Failure)(nil)
)

func TestFailure_StatusCode(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   int
	}{
		{name: "zero status is 422", status: 0, want: http.StatusUnprocessableEntity},
		{name: "explicit status wins", status: http.StatusBadRequest, want: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &Failure{Result: failedResult(t), Status: tt.status}
			if got := f.StatusCode(); got != tt.want {
				t.Errorf("StatusCode = %d, want %d", got, tt.want)
			}
			status, _, ok := contract.StatusOf(fmt.Errorf("wrapped: %w", f))
			if !ok || status != tt.want {
				t.Errorf("StatusOf = %d, %v; want %d, true", status, ok, tt.want)
			}
		})
	}
}

func TestFailure_ErrorsAndChain(t *testing.T) {
	tests := []struct {
		name       string
		failure    *Failure
		wantFields []string
		wantChain  bool
		wantText   string
	}{
		{
			name:       "failed result",
			failure:    NewFailure(failedResult(t)),
			wantFields: []string{"email", "name"},
			wantChain:  true,
			wantText:   "velocity/validation: validation failed: email: The email field must be a valid email address.; name: The name field is required.",
		},
		{
			name:     "nil result from NewFailure",
			failure:  NewFailure(nil),
			wantText: "velocity/validation: validation failed",
		},
		{
			name:     "literal with no result",
			failure:  &Failure{},
			wantText: "velocity/validation: validation failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := tt.failure
			if f.ShouldReport() {
				t.Error("ShouldReport = true, want false")
			}
			if got := f.Error(); got != tt.wantText {
				t.Errorf("Error = %q, want %q", got, tt.wantText)
			}
			errs := f.Errors()
			if len(errs) != len(tt.wantFields) {
				t.Fatalf("Errors = %v, want fields %v", errs, tt.wantFields)
			}
			for _, field := range tt.wantFields {
				if len(errs[field]) == 0 {
					t.Errorf("Errors has no message for %q", field)
				}
			}
			var err error = f
			if got := errors.Is(err, ErrValidationFailed); got != tt.wantChain {
				t.Errorf("errors.Is(ErrValidationFailed) = %v, want %v", got, tt.wantChain)
			}
			var verr ValidationErrors
			if got := errors.As(err, &verr); got != tt.wantChain {
				t.Errorf("errors.As(ValidationErrors) = %v, want %v", got, tt.wantChain)
			}
			if tt.wantChain && verr.First("email") == "" {
				t.Error("ValidationErrors carries no email message")
			}
		})
	}
}

func TestFailure_ErrorBag(t *testing.T) {
	tests := []struct {
		name string
		bag  string
	}{
		{name: "top level", bag: ""},
		{name: "named bag", bag: "login"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &Failure{Result: failedResult(t), Bag: tt.bag}
			if got := f.ErrorBag(); got != tt.bag {
				t.Errorf("ErrorBag = %q, want %q", got, tt.bag)
			}
		})
	}
}

func TestFailure_AsFromWrapped(t *testing.T) {
	want := NewFailure(failedResult(t))
	err := fmt.Errorf("store user: %w", want)
	var got *Failure
	if !errors.As(err, &got) || got != want {
		t.Fatalf("errors.As = %v, want the wrapped failure", got)
	}
}
