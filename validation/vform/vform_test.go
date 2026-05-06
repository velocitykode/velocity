package vform

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/validation"
)

// ---------------------------------------------------------------------------
// Test fixtures
// ---------------------------------------------------------------------------

// signupRequest exercises the canonical FormRequest shape: it returns
// validation.Rules (the slice form), satisfying the FormRequest interface.
type signupRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (signupRequest) Rules() validation.Rules {
	return validation.Rules{
		"email":    {"required", "email"},
		"password": {"required", "min:8"},
	}
}

// signupRequestWithMessages bolts custom messages onto signupRequest.
type signupRequestWithMessages struct {
	signupRequest
}

func (signupRequestWithMessages) ValidationMessages() map[string]string {
	return map[string]string{
		"email.required": "Tell us your email.",
		"password.min":   "Password too short.",
	}
}

// signupPipeRequest exercises the legacy compatibility path: a single slice
// element that itself contains pipe-delimited tokens.
type signupPipeRequest struct {
	Email string `json:"email"`
}

func (signupPipeRequest) Rules() validation.Rules {
	return validation.Rules{
		"email": {"required|email"},
	}
}

// nonValidating has no Rules() method; Validate[T] should skip validation
// entirely and return the bound *T.
type nonValidating struct {
	Name string `json:"name"`
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// jsonCtx builds a *router.Context with a JSON body, Content-Type set, and
// a minimal Services container so ctx.DB() and ctx.View() return safe nils
// without panicking. Tests that need a real DB attach one via SetServices
// directly.
func jsonCtx(t *testing.T, body string) (*router.Context, *httptest.ResponseRecorder) {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	c := router.NewContext(w, r)
	c.SetServices(&app.Services{})
	return c, w
}

// ---------------------------------------------------------------------------
// Validate[T]: happy path
// ---------------------------------------------------------------------------

func TestValidate_Success_ReturnsTAndNilResult(t *testing.T) {
	ctx, _ := jsonCtx(t, `{"email":"a@b.com","password":"longenough"}`)

	form, result, err := Validate[signupRequest](ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil *Result on success, got %+v", result)
	}
	if form == nil || form.Email != "a@b.com" || form.Password != "longenough" {
		t.Fatalf("expected form populated, got %+v", form)
	}
}

func TestValidate_NonFormRequest_SkipsValidation(t *testing.T) {
	ctx, _ := jsonCtx(t, `{"name":"Ali"}`)
	form, result, err := Validate[nonValidating](ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil *Result for non-FormRequest, got %+v", result)
	}
	if form == nil || form.Name != "Ali" {
		t.Fatalf("expected form bound, got %+v", form)
	}
}

// ---------------------------------------------------------------------------
// Validate[T]: error paths
// ---------------------------------------------------------------------------

func TestValidate_ValidationFailure_ReturnsResultNoError(t *testing.T) {
	ctx, w := jsonCtx(t, `{"email":"not-an-email","password":"short"}`)

	form, result, err := Validate[signupRequest](ctx)
	if err != nil {
		t.Fatalf("expected nil error for validation failure, got %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil *Result on validation failure")
	}
	if !result.HasErrors() {
		t.Fatal("expected result.HasErrors() == true")
	}
	all := result.All()
	if _, ok := all["email"]; !ok {
		t.Errorf("expected email error key; got keys=%v", keysOf(all))
	}
	if _, ok := all["password"]; !ok {
		t.Errorf("expected password error key; got keys=%v", keysOf(all))
	}
	// Validate[T] MUST NOT redirect or write a response.
	if w.Code != http.StatusOK {
		t.Errorf("expected no response written, got status=%d", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Errorf("expected empty response body, got %q", w.Body.String())
	}
	// On failure, the *T contract is "zero value", caller should not consume.
	if form == nil {
		t.Error("Validate must still return a *T (zero value) on failure")
	}
}

func TestValidate_MalformedJSON_ReturnsError(t *testing.T) {
	// Malformed JSON yields an empty data map at extraction, which means
	// validation runs and reports required-field errors. This is consistent
	// with bond's flash flow: a malformed body looks like an empty form.
	// We verify that this path doesn't blow up and surfaces a *Result.
	ctx, _ := jsonCtx(t, `{not json`)

	form, result, err := Validate[signupRequest](ctx)
	if err != nil {
		t.Fatalf("expected nil error for malformed JSON (validation-style failure), got %v", err)
	}
	if result == nil || !result.HasErrors() {
		t.Fatal("expected *Result with errors for malformed JSON body")
	}
	// form is the zero-value *T, fields should be empty
	if form == nil || form.Email != "" || form.Password != "" {
		t.Errorf("expected zero-value form, got %+v", form)
	}
}

func TestValidate_EmptyBody_ReportsRequiredErrors(t *testing.T) {
	ctx, _ := jsonCtx(t, ``)

	_, result, err := Validate[signupRequest](ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || !result.HasErrors() {
		t.Fatal("expected required-field errors on empty body")
	}
	if result.First("email") == "" {
		t.Error("expected email required error")
	}
	if result.First("password") == "" {
		t.Error("expected password required error")
	}
}

func TestValidate_MultipleRulesPerField_StopsAtFirstError(t *testing.T) {
	// password has both required and min:8. With "" we should see required,
	// not min, validator stops at first error per field.
	ctx, _ := jsonCtx(t, `{"email":"a@b.com","password":""}`)

	_, result, err := Validate[signupRequest](ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected validation failure")
	}
	msg := result.First("password")
	if !strings.Contains(strings.ToLower(msg), "required") {
		t.Errorf("expected 'required' in message, got %q", msg)
	}
}

// ---------------------------------------------------------------------------
// Validate[T]: legacy pipe-string compatibility
// ---------------------------------------------------------------------------

func TestValidate_PipeStringInSliceElement_StillWorks(t *testing.T) {
	// signupPipeRequest uses {"required|email"}: single slice element with
	// pipe-delimited rules. The validator's parseRuleSlice splits on '|'
	// so this remains backward-compatible.
	ctx, _ := jsonCtx(t, `{"email":"a@b.com"}`)

	_, result, err := Validate[signupPipeRequest](ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil *Result, got %+v", result.All())
	}

	// Now test failure: invalid email
	ctx2, _ := jsonCtx(t, `{"email":"not-email"}`)
	_, result2, _ := Validate[signupPipeRequest](ctx2)
	if result2 == nil || !result2.HasErrors() {
		t.Fatal("expected validation failure for pipe-string rules")
	}
	if result2.First("email") == "" {
		t.Error("expected email error")
	}
}

// ---------------------------------------------------------------------------
// Validate[T]: WithMessages plumbing
// ---------------------------------------------------------------------------

func TestValidate_CustomMessages_AreApplied(t *testing.T) {
	ctx, _ := jsonCtx(t, `{}`)

	_, result, err := Validate[signupRequestWithMessages](ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected validation failure")
	}
	if got := result.First("email"); got != "Tell us your email." {
		t.Errorf("expected custom message for email, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Form[T]: still redirects on failure
// ---------------------------------------------------------------------------

func TestForm_Success_ReturnsT(t *testing.T) {
	ctx, w := jsonCtx(t, `{"email":"a@b.com","password":"longenough"}`)

	form, err := Form[signupRequest](ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if form == nil || form.Email != "a@b.com" {
		t.Fatalf("expected form populated, got %+v", form)
	}
	// No redirect on success.
	if w.Code != http.StatusOK {
		t.Errorf("expected status 200 (no body written), got %d", w.Code)
	}
}

func TestForm_Failure_ReturnsErrValidationAborted(t *testing.T) {
	ctx, _ := jsonCtx(t, `{"email":"bad","password":"x"}`)

	form, err := Form[signupRequest](ctx)
	if !errors.Is(err, router.ErrValidationAborted) {
		t.Fatalf("expected router.ErrValidationAborted, got %v", err)
	}
	if form != nil {
		t.Errorf("expected nil form on failure, got %+v", form)
	}
}

func TestForm_Failure_FlashesErrorCookie(t *testing.T) {
	ctx, w := jsonCtx(t, `{"email":"bad","password":"x"}`)

	_, _ = Form[signupRequest](ctx)

	// _velocity_errors flash cookie should be set by ctx.WithErrors.
	cookies := w.Result().Cookies()
	var foundErrors bool
	for _, c := range cookies {
		if c.Name == "_velocity_errors" {
			foundErrors = true
			if c.Value == "" {
				t.Error("expected non-empty errors cookie value")
			}
		}
	}
	if !foundErrors {
		t.Errorf("expected _velocity_errors cookie to be set; got cookies=%v", cookieNames(cookies))
	}
}

// ---------------------------------------------------------------------------
// Concurrency: Validate[T] must be safe under race
// ---------------------------------------------------------------------------

func TestValidate_Concurrent_NoRaceUnderLoad(t *testing.T) {
	const n = 64
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			body := `{"email":"a@b.com","password":"longenough"}`
			if i%2 == 0 {
				body = `{"email":"bad","password":"x"}`
			}
			ctx, _ := jsonCtx(t, body)
			_, result, err := Validate[signupRequest](ctx)
			if err != nil {
				t.Errorf("iteration %d: unexpected error: %v", i, err)
				return
			}
			if i%2 == 0 {
				if result == nil || !result.HasErrors() {
					t.Errorf("iteration %d: expected failure, got success", i)
				}
			} else {
				if result != nil {
					t.Errorf("iteration %d: expected success, got %+v", i, result.All())
				}
			}
		}(i)
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// Type-system tests for item 13: Rules type unification
// ---------------------------------------------------------------------------

// Compile-time assertion: a method that returns validation.Rules in the slice
// form satisfies vform.FormRequest. If anyone breaks the FormRequest
// signature this test stops compiling.
type aliasFormRequest struct{}

func (aliasFormRequest) Rules() validation.Rules {
	return validation.Rules{
		"email": {"required", "email"},
	}
}

var _ FormRequest = aliasFormRequest{}

// Compile-time assertion: NewRules converts PipeRules to the canonical Rules
// type, and the result satisfies the FormRequest contract.
type pipeFormRequest struct{}

func (pipeFormRequest) Rules() validation.Rules {
	return validation.NewRules(validation.PipeRules{
		"email": "required|email",
	})
}

var _ FormRequest = pipeFormRequest{}

func TestRules_AliasForm_CompilesAndValidates(t *testing.T) {
	ctx, _ := jsonCtx(t, `{"email":"bad"}`)
	_, result, err := Validate[aliasFormRequest](ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || !result.HasErrors() {
		t.Fatal("expected validation failure")
	}
}

func TestRules_PipeFormViaNewRules_CompilesAndValidates(t *testing.T) {
	ctx, _ := jsonCtx(t, `{"email":"bad"}`)
	_, result, err := Validate[pipeFormRequest](ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || !result.HasErrors() {
		t.Fatal("expected validation failure")
	}
}

func TestRules_DirectLiteralAndNewRules_AreEquivalent(t *testing.T) {
	a := validation.Rules{
		"email": {"required", "email"},
	}
	b := validation.NewRules(validation.PipeRules{
		"email": "required|email",
	})
	if len(a) != len(b) {
		t.Fatalf("len mismatch: a=%d b=%d", len(a), len(b))
	}
	if len(a["email"]) != 2 || len(b["email"]) != 2 {
		t.Fatalf("rule slice length mismatch: a=%v b=%v", a["email"], b["email"])
	}
	for i := range a["email"] {
		if a["email"][i] != b["email"][i] {
			t.Errorf("token %d: a=%q b=%q", i, a["email"][i], b["email"][i])
		}
	}
}

func TestNewRules_NilInput_ReturnsNil(t *testing.T) {
	if got := validation.NewRules(nil); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestNewRules_EmptyMap_ReturnsEmptyRules(t *testing.T) {
	got := validation.NewRules(validation.PipeRules{})
	if got == nil {
		t.Fatal("expected non-nil empty Rules")
	}
	if len(got) != 0 {
		t.Errorf("expected empty Rules, got %v", got)
	}
}

func TestNewRules_DropsEmptyTokens(t *testing.T) {
	got := validation.NewRules(validation.PipeRules{
		"x": "required||min:3| ",
	})
	if len(got["x"]) != 2 {
		t.Errorf("expected 2 tokens after dropping empties, got %v", got["x"])
	}
	if got["x"][0] != "required" || got["x"][1] != "min:3" {
		t.Errorf("unexpected tokens: %v", got["x"])
	}
}

// ---------------------------------------------------------------------------
// utility helpers
// ---------------------------------------------------------------------------

func keysOf[K comparable, V any](m map[K]V) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func cookieNames(cs []*http.Cookie) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Name
	}
	return out
}
