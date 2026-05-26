package bond

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/router"
)

// testFlashEncryptor returns a fresh AES-256-GCM encryptor for flash tests.
// A 32-byte base64 key is used so the default cipher is exercised end to end.
func testFlashEncryptor(t *testing.T) crypto.Encryptor {
	t.Helper()
	key := "base64:MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE="
	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    key,
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("failed to initialize flash encryptor: %v", err)
	}
	return enc
}

// requestWithServices returns a GET / request with an *app.Services
// carrying enc stashed on its context via router.WithServices. This is
// the wiring that the production pipeline performs; tests reproduce it
// because bond's flash read path discovers the encryptor via that key.
func requestWithServices(t *testing.T, enc crypto.Encryptor) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	return router.WithServices(r, &app.Services{Crypto: enc})
}

// sealCookie returns an authenticated cookie value for name produced
// under enc, matching what router.Context.WithErrors / WithInput emit
// on the wire.
func sealCookie(t *testing.T, enc crypto.Encryptor, name string, value any) string {
	t.Helper()
	v, err := router.SealFlash(enc, name, value)
	if err != nil {
		t.Fatalf("SealFlash: %v", err)
	}
	return v
}

// requestWithFlash builds an *http.Request carrying authenticated flash
// cookies under enc, with enc reachable from the request context.
func requestWithFlash(t *testing.T, enc crypto.Encryptor, errors, old any) *http.Request {
	t.Helper()
	r := requestWithServices(t, enc)
	if errors != nil {
		r.AddCookie(&http.Cookie{Name: flashErrorsCookie, Value: sealCookie(t, enc, flashErrorsCookie, errors)})
	}
	if old != nil {
		r.AddCookie(&http.Cookie{Name: flashInputCookie, Value: sealCookie(t, enc, flashInputCookie, old)})
	}
	return r
}

// --- readFlashCookie ---

func TestReadFlashCookie_ReturnsDecodedValue(t *testing.T) {
	enc := testFlashEncryptor(t)
	errors := map[string]any{
		"email": "The email field is required.",
		"name":  "The name field is required.",
	}
	r := requestWithFlash(t, enc, errors, nil)

	result, ok := readFlashCookie(r, flashErrorsCookie)
	if !ok {
		t.Fatal("expected ok to be true")
	}

	m, _ := result.(map[string]any)
	if m["email"] != "The email field is required." {
		t.Errorf("expected email error, got %v", m["email"])
	}
	if m["name"] != "The name field is required." {
		t.Errorf("expected name error, got %v", m["name"])
	}
}

func TestReadFlashCookie_MissingCookie(t *testing.T) {
	enc := testFlashEncryptor(t)
	r := requestWithServices(t, enc)

	result, ok := readFlashCookie(r, flashErrorsCookie)
	if ok {
		t.Errorf("expected ok to be false for missing cookie, got result: %v", result)
	}
}

func TestReadFlashCookie_EmptyValue(t *testing.T) {
	enc := testFlashEncryptor(t)
	r := requestWithServices(t, enc)
	r.AddCookie(&http.Cookie{Name: flashErrorsCookie, Value: ""})

	result, ok := readFlashCookie(r, flashErrorsCookie)
	if ok {
		t.Errorf("expected ok to be false for empty cookie, got result: %v", result)
	}
}

func TestReadFlashCookie_TamperedCiphertext(t *testing.T) {
	enc := testFlashEncryptor(t)
	r := requestWithServices(t, enc)
	// Valid envelope-shaped string but no key produces this, ever, so
	// authentication must fail.
	r.AddCookie(&http.Cookie{Name: flashErrorsCookie, Value: "v1:bm90LWEtdmFsaWQtY2lwaGVydGV4dA=="})

	result, ok := readFlashCookie(r, flashErrorsCookie)
	if ok {
		t.Errorf("expected ok to be false for tampered cookie, got result: %v", result)
	}
	if result != nil {
		t.Errorf("expected nil result on auth failure, got %v", result)
	}
}

func TestReadFlashCookie_InvalidBase64(t *testing.T) {
	enc := testFlashEncryptor(t)
	r := requestWithServices(t, enc)
	r.AddCookie(&http.Cookie{Name: flashErrorsCookie, Value: "%%%not-base64%%%"})

	result, ok := readFlashCookie(r, flashErrorsCookie)
	if ok {
		t.Errorf("expected ok to be false for invalid base64, got result: %v", result)
	}
}

func TestReadFlashCookie_NoEncryptorWired(t *testing.T) {
	// Even with a syntactically valid (signed under some other key)
	// cookie, when the request never went through velocity.New() the
	// read path has no encryptor to verify against and must refuse.
	enc := testFlashEncryptor(t)
	cookieValue := sealCookie(t, enc, flashErrorsCookie, map[string]any{"k": "v"})

	// Build a request that has NO services attached.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: flashErrorsCookie, Value: cookieValue})

	result, ok := readFlashCookie(r, flashErrorsCookie)
	if ok {
		t.Errorf("expected ok to be false without encryptor, got result: %v", result)
	}
}

func TestReadFlashCookie_WrongKeyInvalidates(t *testing.T) {
	// Sealed under key A, read under key B (e.g. APP_KEY rotated and
	// previous key dropped). The cookie must NOT decode.
	encA := testFlashEncryptor(t)
	cookieValue := sealCookie(t, encA, flashErrorsCookie, map[string]any{"k": "v"})

	encB, err := crypto.NewEncryptor(crypto.Config{
		Key:    "base64:YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE=", // different key, 32 bytes
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("failed to build second encryptor: %v", err)
	}
	r := requestWithServices(t, encB)
	r.AddCookie(&http.Cookie{Name: flashErrorsCookie, Value: cookieValue})

	result, ok := readFlashCookie(r, flashErrorsCookie)
	if ok {
		t.Errorf("expected key-rotation invalidation, got %v", result)
	}
}

func TestReadFlashCookie_AADCrossBindingRejected(t *testing.T) {
	// A ciphertext sealed for the errors slot must NOT decrypt when
	// presented under the old-input cookie name (and vice versa).
	enc := testFlashEncryptor(t)
	errorsCookie := sealCookie(t, enc, flashErrorsCookie, map[string]any{"email": "required"})

	r := requestWithServices(t, enc)
	// Put the errors ciphertext into the input cookie slot.
	r.AddCookie(&http.Cookie{Name: flashInputCookie, Value: errorsCookie})

	result, ok := readFlashCookie(r, flashInputCookie)
	if ok {
		t.Errorf("expected AAD mismatch to reject cross-slot replay, got %v", result)
	}
}

func TestReadFlashCookie_OversizedRejected(t *testing.T) {
	enc := testFlashEncryptor(t)
	// Build a cookie value larger than MaxFlashCookieSize. We must not
	// pay the cost of attempting decryption on it.
	huge := strings.Repeat("A", router.MaxFlashCookieSize+1)
	r := requestWithServices(t, enc)
	r.AddCookie(&http.Cookie{Name: flashErrorsCookie, Value: huge})

	result, ok := readFlashCookie(r, flashErrorsCookie)
	if ok {
		t.Errorf("expected oversized cookie to be rejected, got %v", result)
	}
}

func TestReadFlashCookie_StringValue(t *testing.T) {
	enc := testFlashEncryptor(t)
	r := requestWithServices(t, enc)
	r.AddCookie(&http.Cookie{Name: flashErrorsCookie, Value: sealCookie(t, enc, flashErrorsCookie, "simple string")})

	result, ok := readFlashCookie(r, flashErrorsCookie)
	if !ok {
		t.Fatal("expected ok to be true")
	}
	if result != "simple string" {
		t.Errorf("expected 'simple string', got %v", result)
	}
}

func TestReadFlashCookie_ArrayValue(t *testing.T) {
	enc := testFlashEncryptor(t)
	r := requestWithServices(t, enc)
	r.AddCookie(&http.Cookie{Name: flashErrorsCookie, Value: sealCookie(t, enc, flashErrorsCookie, []string{"err1", "err2"})})

	result, ok := readFlashCookie(r, flashErrorsCookie)
	if !ok {
		t.Fatal("expected ok to be true")
	}
	arr, _ := result.([]any)
	if len(arr) != 2 {
		t.Fatalf("expected 2 elements, got %d", len(arr))
	}
	if arr[0] != "err1" || arr[1] != "err2" {
		t.Errorf("unexpected array values: %v", arr)
	}
}

// --- applyFlashData ---

func TestApplyFlashData_SetsErrorsInProps(t *testing.T) {
	enc := testFlashEncryptor(t)
	errors := map[string]any{"email": "required"}
	r := requestWithFlash(t, enc, errors, nil)
	w := httptest.NewRecorder()
	props := Props{}

	applyFlashData(w, r, props)

	errs, ok := props["errors"].(map[string]any)
	if !ok {
		t.Fatalf("expected errors to be map[string]any, got %T", props["errors"])
	}
	if errs["email"] != "required" {
		t.Errorf("expected email error 'required', got %v", errs["email"])
	}
}

func TestApplyFlashData_SetsOldInputInProps(t *testing.T) {
	enc := testFlashEncryptor(t)
	old := map[string]any{"name": "Ali", "email": "ali@example.com"}
	r := requestWithFlash(t, enc, nil, old)
	w := httptest.NewRecorder()
	props := Props{}

	applyFlashData(w, r, props)

	input, ok := props["old"].(map[string]any)
	if !ok {
		t.Fatalf("expected old to be map[string]any, got %T", props["old"])
	}
	if input["name"] != "Ali" {
		t.Errorf("expected name 'Ali', got %v", input["name"])
	}
	if input["email"] != "ali@example.com" {
		t.Errorf("expected email 'ali@example.com', got %v", input["email"])
	}
}

func TestApplyFlashData_SetsBothErrorsAndOldInput(t *testing.T) {
	enc := testFlashEncryptor(t)
	errors := map[string]any{"email": "invalid format"}
	old := map[string]any{"email": "bad-email"}
	r := requestWithFlash(t, enc, errors, old)
	w := httptest.NewRecorder()
	props := Props{}

	applyFlashData(w, r, props)

	if props["errors"] == nil {
		t.Error("expected errors to be set")
	}
	if props["old"] == nil {
		t.Error("expected old input to be set")
	}
}

func TestApplyFlashData_OverridesExistingProps(t *testing.T) {
	enc := testFlashEncryptor(t)
	errors := map[string]any{"email": "flash error"}
	r := requestWithFlash(t, enc, errors, nil)
	w := httptest.NewRecorder()
	props := Props{
		"errors": map[string]any{"name": "original error"},
	}

	applyFlashData(w, r, props)

	errs := props["errors"].(map[string]any)
	if _, hasName := errs["name"]; hasName {
		t.Error("expected flash errors to override, but original 'name' key still present")
	}
	if errs["email"] != "flash error" {
		t.Errorf("expected email 'flash error', got %v", errs["email"])
	}
}

func TestApplyFlashData_NoFlashCookies(t *testing.T) {
	enc := testFlashEncryptor(t)
	r := requestWithServices(t, enc)
	w := httptest.NewRecorder()
	props := Props{"existing": "value"}

	applyFlashData(w, r, props)

	// Existing props untouched
	if props["existing"] != "value" {
		t.Errorf("expected existing prop to remain, got %v", props["existing"])
	}
	// No errors or old keys added
	if _, ok := props["errors"]; ok {
		t.Error("expected no 'errors' key in props")
	}
	if _, ok := props["old"]; ok {
		t.Error("expected no 'old' key in props")
	}
}

// --- clearFlashCookies ---

func TestClearFlashCookies_ExpiresBothCookies(t *testing.T) {
	w := httptest.NewRecorder()

	clearFlashCookies(w)

	cookies := w.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("expected 2 clear cookies, got %d", len(cookies))
	}

	found := map[string]bool{}
	for _, c := range cookies {
		found[c.Name] = true
		if c.MaxAge != -1 {
			t.Errorf("cookie %s: expected MaxAge -1, got %d", c.Name, c.MaxAge)
		}
		if c.Value != "" {
			t.Errorf("cookie %s: expected empty value, got %q", c.Name, c.Value)
		}
		if !c.HttpOnly {
			t.Errorf("cookie %s: expected HttpOnly", c.Name)
		}
		if !c.Secure {
			t.Errorf("cookie %s: expected Secure", c.Name)
		}
		if c.SameSite != http.SameSiteLaxMode {
			t.Errorf("cookie %s: expected SameSiteLax", c.Name)
		}
		if c.Path != "/" {
			t.Errorf("cookie %s: expected path '/', got %q", c.Name, c.Path)
		}
	}

	if !found[flashErrorsCookie] {
		t.Errorf("expected %s cookie to be cleared", flashErrorsCookie)
	}
	if !found[flashInputCookie] {
		t.Errorf("expected %s cookie to be cleared", flashInputCookie)
	}
}

// --- Single-use behavior (flash cleared after reading) ---

func TestApplyFlashData_ClearsCookiesAfterReading(t *testing.T) {
	enc := testFlashEncryptor(t)
	errors := map[string]any{"field": "error"}
	r := requestWithFlash(t, enc, errors, nil)
	w := httptest.NewRecorder()
	props := Props{}

	applyFlashData(w, r, props)

	// Verify that clear cookies were set on the response
	cookies := w.Result().Cookies()
	var errorsClear, inputClear bool
	for _, c := range cookies {
		if c.Name == flashErrorsCookie && c.MaxAge == -1 {
			errorsClear = true
		}
		if c.Name == flashInputCookie && c.MaxAge == -1 {
			inputClear = true
		}
	}
	if !errorsClear {
		t.Error("expected errors cookie to be cleared after reading")
	}
	if !inputClear {
		t.Error("expected input cookie to be cleared after reading")
	}
}

func TestApplyFlashData_DoesNotClearWhenNoFlashData(t *testing.T) {
	enc := testFlashEncryptor(t)
	r := requestWithServices(t, enc)
	w := httptest.NewRecorder()
	props := Props{}

	applyFlashData(w, r, props)

	cookies := w.Result().Cookies()
	if len(cookies) != 0 {
		t.Errorf("expected no Set-Cookie headers when no flash data, got %d", len(cookies))
	}
}

func TestApplyFlashData_SecondReadHasNoFlashData(t *testing.T) {
	enc := testFlashEncryptor(t)
	errors := map[string]any{"field": "error"}
	old := map[string]any{"field": "value"}
	r := requestWithFlash(t, enc, errors, old)
	w := httptest.NewRecorder()
	props := Props{}

	// First read applies flash data
	applyFlashData(w, r, props)
	if props["errors"] == nil {
		t.Fatal("expected errors on first read")
	}
	if props["old"] == nil {
		t.Fatal("expected old input on first read")
	}

	// Simulate a second request that does NOT carry the flash cookies
	// (browser would have deleted them because MaxAge=-1)
	r2 := requestWithServices(t, enc)
	w2 := httptest.NewRecorder()
	props2 := Props{}

	applyFlashData(w2, r2, props2)

	if _, ok := props2["errors"]; ok {
		t.Error("expected no errors on second read (flash should be consumed)")
	}
	if _, ok := props2["old"]; ok {
		t.Error("expected no old input on second read (flash should be consumed)")
	}
}

// --- Nil and empty data edge cases ---

func TestApplyFlashData_EmptyErrorsMap(t *testing.T) {
	enc := testFlashEncryptor(t)
	// An empty map is still valid JSON, should be applied.
	errors := map[string]any{}
	r := requestWithFlash(t, enc, errors, nil)
	w := httptest.NewRecorder()
	props := Props{}

	applyFlashData(w, r, props)

	if props["errors"] == nil {
		t.Error("expected empty map to be set as errors prop")
	}
}

func TestApplyFlashData_NullJSON(t *testing.T) {
	enc := testFlashEncryptor(t)
	// Seal an explicit null value; JSON null decodes to nil, which is
	// still a successful authenticated read (the cookie was sealed by
	// the same encryptor).
	r := requestWithServices(t, enc)
	r.AddCookie(&http.Cookie{Name: flashErrorsCookie, Value: sealCookie(t, enc, flashErrorsCookie, nil)})
	w := httptest.NewRecorder()
	props := Props{}

	applyFlashData(w, r, props)

	if _, ok := props["errors"]; !ok {
		t.Error("expected errors key to be set even for JSON null")
	}
}

// TestFlash_EndToEnd_RouterSealBondOpen exercises the full path: a
// router.Context seals the cookie via WithErrors, the response carries
// the Set-Cookie, a subsequent request replays the cookie, bond's
// Render consumes it via applyFlashData, and the resulting page props
// carry the original errors map. This is the bond integration test
// the audit requires before declaring the regression fixed.
func TestFlash_EndToEnd_RouterSealBondOpen(t *testing.T) {
	enc := testFlashEncryptor(t)

	// --- POST handler seals the flash via WithErrors ---
	postW := httptest.NewRecorder()
	postR := httptest.NewRequest(http.MethodPost, "/signup", nil)
	postR = router.WithServices(postR, &app.Services{Crypto: enc})
	postCtx := router.NewContext(postW, postR)
	postCtx.SetServices(&app.Services{Crypto: enc})

	wantErrors := map[string]any{"email": "must be valid", "name": "required"}
	postCtx.WithErrors(wantErrors)
	postCtx.WithInput(map[string]any{"email": "bogus"})

	// Pull the Set-Cookie headers off the POST response.
	setCookies := postW.Result().Cookies()
	if len(setCookies) != 2 {
		t.Fatalf("expected 2 cookies from WithErrors+WithInput, got %d: %#v", len(setCookies), setCookies)
	}

	// --- GET request carries the cookies back and renders via bond ---
	b := setupBond(t)
	w := httptest.NewRecorder()
	r := requestWithServices(t, enc)
	r.Header.Set("X-Inertia", "true")
	for _, c := range setCookies {
		r.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}

	if err := b.Render(w, r, "Signup", Props{}); err != nil {
		t.Fatalf("Render: %v", err)
	}

	// The rendered page must surface the flashed errors as a prop.
	var page Page
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	gotErrors, ok := page.Props["errors"].(map[string]any)
	if !ok {
		t.Fatalf("expected page.Props[errors] map, got %T (%v)", page.Props["errors"], page.Props["errors"])
	}
	if gotErrors["email"] != "must be valid" || gotErrors["name"] != "required" {
		t.Errorf("errors prop mismatch: %v", gotErrors)
	}
	if got, _ := page.Props["old"].(map[string]any); got == nil || got["email"] != "bogus" {
		t.Errorf("old prop missing or wrong: %v", page.Props["old"])
	}

	// And the GET response must clear the consumed cookies so the next
	// render does not double-fire.
	clearSeen := map[string]bool{}
	for _, c := range w.Result().Cookies() {
		if c.MaxAge == -1 && c.Value == "" {
			clearSeen[c.Name] = true
		}
	}
	if !clearSeen[flashErrorsCookie] || !clearSeen[flashInputCookie] {
		t.Errorf("expected both flash cookies cleared, got %v", clearSeen)
	}
}

func TestReadFlashCookie_NestedData(t *testing.T) {
	enc := testFlashEncryptor(t)
	nested := map[string]any{
		"email": []any{"required", "must be valid"},
		"address": map[string]any{
			"city": "required",
		},
	}
	r := requestWithFlash(t, enc, nested, nil)

	result, ok := readFlashCookie(r, flashErrorsCookie)
	if !ok {
		t.Fatal("expected ok to be true")
	}

	m := result.(map[string]any)
	emailErrs := m["email"].([]any)
	if len(emailErrs) != 2 {
		t.Fatalf("expected 2 email errors, got %d", len(emailErrs))
	}
	addr := m["address"].(map[string]any)
	if addr["city"] != "required" {
		t.Errorf("expected address.city 'required', got %v", addr["city"])
	}
}
