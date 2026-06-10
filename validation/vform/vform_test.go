package vform

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/validation"
)

// testFormEncryptor returns an AES-256-GCM encryptor for vform tests
// that exercise flash-cookie emission. Sealing requires an encryptor;
// without one, ctx.WithErrors no-ops and the cookie is never set.
func testFormEncryptor(t *testing.T) crypto.Encryptor {
	t.Helper()
	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    "base64:MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=",
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("failed to build encryptor: %v", err)
	}
	return enc
}

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
	// Wire a real encryptor so ctx.WithErrors / WithInput can seal the
	// flash cookies. Tests that need a DB still attach one via
	// SetServices directly; they can preserve Crypto by reading it
	// off the existing services first.
	c.SetServices(&app.Services{Crypto: testFormEncryptor(t)})
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
// Validate[T]: plain map return type (alias regression guard)
// ---------------------------------------------------------------------------

// plainMapRulesRequest declares Rules with the underlying map[string][]string
// type rather than the canonical validation.Rules alias. Before validation.Rules
// became a type alias, this exact shape silently failed the FormRequest
// interface assertion and validation was skipped entirely. The compile-time
// _ = FormRequest(...) assertion plus this runtime test guard against that
// regression.
type plainMapRulesRequest struct {
	Email                string `json:"email"`
	Password             string `json:"password"`
	PasswordConfirmation string `json:"password_confirmation"`
}

func (plainMapRulesRequest) Rules() map[string][]string {
	return map[string][]string{
		"email":    {"required", "email"},
		"password": {"required", "min:8", "confirmed"},
	}
}

// Compile-time check: plain map return type must satisfy FormRequest.
var _ FormRequest = plainMapRulesRequest{}

func TestValidate_PlainMapRulesReturnType_RunsValidation(t *testing.T) {
	// password fails "confirmed" (no matching confirmation). If the alias
	// fix regresses, this returns nil *Result and the assertion below trips.
	ctx, _ := jsonCtx(t, `{"email":"a@b.com","password":"longenough","password_confirmation":"different"}`)

	_, result, err := Validate[plainMapRulesRequest](ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("validation skipped: Rules() map[string][]string did not satisfy FormRequest interface (alias regression)")
	}
	if !result.HasErrors() {
		t.Fatal("expected confirmed-rule failure")
	}
	if result.First("password") == "" {
		t.Error("expected password confirmation error")
	}
}

// ---------------------------------------------------------------------------
// Validate[T]: mismatched-shape Rules method guardrail
// ---------------------------------------------------------------------------

// badRulesShape declares a Rules method with an incompatible return type.
// The guardrail in Validate[T] must report this loudly instead of falling
// through to a silent bind-without-validation.
type badRulesShape struct {
	Name string `json:"name"`
}

func (badRulesShape) Rules() string { return "required" }

func TestValidate_MismatchedRulesSignature_ReturnsLoudError(t *testing.T) {
	ctx, _ := jsonCtx(t, `{"name":"x"}`)

	_, _, err := Validate[badRulesShape](ctx)
	if err == nil {
		t.Fatal("expected loud error for mismatched Rules() signature, got nil")
	}
	if !strings.Contains(err.Error(), "Rules method") {
		t.Errorf("expected error to mention Rules method, got %q", err.Error())
	}
}

// noRulesMethod has no Rules method at all. Guardrail must not fire and
// Validate must skip validation per the existing nonValidating contract.
type noRulesMethod struct {
	Name string `json:"name"`
}

func TestValidate_NoRulesMethod_GuardrailDoesNotFire(t *testing.T) {
	ctx, _ := jsonCtx(t, `{"name":"Ali"}`)

	form, result, err := Validate[noRulesMethod](ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil *Result, got %+v", result)
	}
	if form == nil || form.Name != "Ali" {
		t.Fatalf("expected form bound, got %+v", form)
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

// ---------------------------------------------------------------------------
// safeDB: non-orm contract.Database must yield nil, not panic
// ---------------------------------------------------------------------------

// stubContractDB satisfies contract.Database but deliberately is NOT an
// *orm.Manager and does not implement the richer orm.Database, modeling an
// adopter that installs a different database implementation.
type stubContractDB struct{}

func (stubContractDB) DB() *sql.DB { return nil }
func (stubContractDB) Raw(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return nil, nil
}
func (stubContractDB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return nil, nil
}
func (stubContractDB) Transaction(ctx context.Context, fn func(ctx context.Context) error) error {
	return nil
}
func (stubContractDB) Begin(ctx context.Context) (*sql.Tx, error)                       { return nil, nil }
func (stubContractDB) Shutdown(ctx context.Context) error                               { return nil }
func (stubContractDB) Ping() error                                                      { return nil }
func (stubContractDB) DriverName() string                                               { return "stub" }
func (stubContractDB) DatabaseName() string                                             { return "stub" }
func (stubContractDB) Stats() sql.DBStats                                               { return sql.DBStats{} }
func (stubContractDB) SetEventDispatcher(fn func(ctx context.Context, event any) error) {}

// compile-time assertion that the stub satisfies contract.Database.
var _ contract.Database = stubContractDB{}

func TestSafeDB_NonORMDatabase_ReturnsNilNoPanic(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	ctx := router.NewContext(w, r)
	ctx.SetServices(&app.Services{DB: stubContractDB{}})

	if got := safeDB(ctx); got != nil {
		t.Fatalf("safeDB() on a non-orm contract.Database = %v, want nil", got)
	}
}
