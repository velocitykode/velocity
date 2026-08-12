package dbrules

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/validation"
)

// ---------------------------------------------------------------------------
// CheckWithDB(), without a real DB (nil): only non-DB rules are tested.
// ---------------------------------------------------------------------------

func TestCheckWithDB_NilDB(t *testing.T) {
	form := url.Values{}
	form.Set("name", "Alice")
	form.Set("email", "alice@example.com")

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	result, err := CheckWithDB(r, validation.Rules{
		"name":  {validation.Required()},
		"email": {validation.Required(), validation.Email()},
	}, nil)
	if err != nil {
		t.Fatalf("unexpected rule-set error: %v", err)
	}

	if result.HasErrors() {
		t.Fatalf("expected no errors, got: %v", result.All())
	}
}

func TestCheckWithDB_NilDB_Invalid(t *testing.T) {
	form := url.Values{}
	form.Set("email", "bad")

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	result, err := CheckWithDB(r, validation.Rules{
		"name":  {validation.Required()},
		"email": {validation.Required(), validation.Email()},
	}, nil)
	if err != nil {
		t.Fatalf("unexpected rule-set error: %v", err)
	}

	if !result.HasErrors() {
		t.Fatal("expected errors")
	}
}

// ---------------------------------------------------------------------------
// CheckDataWithDB() tests
// ---------------------------------------------------------------------------

func TestCheckDataWithDB_NilDB_Valid(t *testing.T) {
	data := map[string]interface{}{
		"name": "Alice",
	}

	result, err := CheckDataWithDB(data, validation.Rules{
		"name": {validation.Required(), validation.Min(3)},
	}, nil)
	if err != nil {
		t.Fatalf("unexpected rule-set error: %v", err)
	}

	if result.HasErrors() {
		t.Fatalf("expected no errors, got: %v", result.All())
	}
}

func TestCheckDataWithDB_NilDB_Invalid(t *testing.T) {
	data := map[string]interface{}{
		"name": "Al",
	}

	result, err := CheckDataWithDB(data, validation.Rules{
		"name": {validation.Required(), validation.Min(3)},
	}, nil)
	if err != nil {
		t.Fatalf("unexpected rule-set error: %v", err)
	}

	if !result.HasErrors() {
		t.Fatal("expected errors")
	}
	if result.First("name") == "" {
		t.Error("expected error for name")
	}
}
