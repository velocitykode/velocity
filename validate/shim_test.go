package validate

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/validation"
)

// TestShim_TypeAliases asserts that the shim's Rules/Messages types are
// still usable interchangeably with the canonical validation package's
// types — this is the contract that keeps the deprecation transparent.
func TestShim_TypeAliases(t *testing.T) {
	// Rules is map[string][]string; validation.Rules is map[string]string.
	// They are *not* identical types (that's the point of the shim) but the
	// shim must accept the expected shape.
	var rules Rules = map[string][]string{
		"name": {"required", "min:3"},
	}
	if rules["name"][0] != "required" {
		t.Fatalf("unexpected rules layout: %v", rules)
	}

	var msgs Messages = map[string]string{
		"name.required": "custom",
	}
	if msgs["name.required"] != "custom" {
		t.Fatalf("unexpected messages layout: %v", msgs)
	}
}

func TestShim_Check_ForwardsToValidation(t *testing.T) {
	form := url.Values{}
	form.Set("name", "Al")
	form.Set("email", "bad")
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	shimErrors := Check(r, Rules{
		"name":  {"required", "min:3"},
		"email": {"required", "email"},
	})

	// Re-run the same request through validation directly and assert that
	// the error sets line up — this is the forwarding invariant.
	r2 := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	canonical := validation.Check(r2, validation.Rules{
		"name":  "required|min:3",
		"email": "required|email",
	})

	if shimErrors.HasErrors() != canonical.HasErrors() {
		t.Fatalf("HasErrors disagree: shim=%v canonical=%v",
			shimErrors.HasErrors(), canonical.HasErrors())
	}
	if !reflect.DeepEqual(shimErrors.Messages(), canonical.Messages()) {
		t.Fatalf("Messages() disagree:\nshim      = %#v\ncanonical = %#v",
			shimErrors.Messages(), canonical.Messages())
	}
	if !reflect.DeepEqual(shimErrors.All(), canonical.All()) {
		t.Fatalf("All() disagree:\nshim      = %#v\ncanonical = %#v",
			shimErrors.All(), canonical.All())
	}
}

func TestShim_CheckData_ForwardsToValidation(t *testing.T) {
	data := map[string]interface{}{
		"name":  "",
		"email": "bad",
	}
	shim := CheckData(data, Rules{
		"name":  {"required"},
		"email": {"required", "email"},
	})
	canonical := validation.CheckData(data, validation.Rules{
		"name":  "required",
		"email": "required|email",
	})
	if shim.HasErrors() != canonical.HasErrors() {
		t.Fatalf("HasErrors disagree")
	}
	if shim.First("email") == "" || canonical.First("email") == "" {
		t.Fatalf("expected email error from both layers")
	}
	if shim.First("email") != canonical.First("email") {
		t.Fatalf("message disagree: shim=%q canonical=%q", shim.First("email"), canonical.First("email"))
	}
}

func TestShim_CheckWithDB_ForwardsToValidation(t *testing.T) {
	// Passing nil DB exercises the same code path on both sides and must
	// not panic.
	body := `{"email":"not-an-email"}`
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(body)))
	r.Header.Set("Content-Type", "application/json")

	shim := CheckWithDB(r, Rules{"email": {"required", "email"}}, nil)
	if !shim.HasErrors() {
		t.Fatal("expected shim to surface validation errors")
	}
}

func TestShim_CustomMessagesForward(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	errs := Check(r, Rules{"title": {"required"}}, Messages{
		"title.required": "A title is mandatory",
	})
	if got := errs.First("title"); got != "A title is mandatory" {
		t.Fatalf("custom message not forwarded: got %q", got)
	}
}

// TestShim_FormRequestInterfaces asserts that all three interface types
// from the shim package still satisfy their documented shapes. If the
// interface definitions ever drift from validation's expectations,
// Form[T] at the call site will stop compiling — this test catches that
// earlier.
func TestShim_FormRequestInterfaces(t *testing.T) {
	var _ FormRequest = (*formTestStruct)(nil)
	var _ WithMessages = (*formTestStruct)(nil)
	var _ WithAuthorization = (*formTestStruct)(nil)
}

type formTestStruct struct{}

func (formTestStruct) Rules() Rules                 { return Rules{} }
func (formTestStruct) ValidationMessages() Messages { return Messages{} }
func (formTestStruct) Authorize() bool              { return true }

// TestShim_ResultToErrors exercises the private resultToErrors adapter to
// confirm it copes with a nil *validation.Result without panicking.
func TestShim_ResultToErrors_NilResult(t *testing.T) {
	e := resultToErrors(nil, nil)
	if e == nil {
		t.Fatal("resultToErrors(nil, nil) should still return an *Errors")
	}
	if e.HasErrors() {
		t.Fatal("nil input should report no errors")
	}
}

func TestShim_WrapErrPrefix(t *testing.T) {
	err := wrapErr("bind failed", errUnderlying)
	if err == nil {
		t.Fatal("wrapErr returned nil")
	}
	got := err.Error()
	if !strings.HasPrefix(got, "velocity/validate: ") {
		t.Fatalf("wrap prefix missing: %q", got)
	}
	if !strings.Contains(got, "bind failed") {
		t.Fatalf("wrap context missing: %q", got)
	}
}

// errUnderlying is a simple stand-in error for wrapping tests.
var errUnderlying = errString("boom")

type errString string

func (e errString) Error() string { return string(e) }
