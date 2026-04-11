package bond

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// encodeFlash encodes a value as base64+JSON, matching the format used by
// router.Context.WithErrors / WithInput.
func encodeFlash(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("failed to marshal flash value: %v", err)
	}
	return base64.URLEncoding.EncodeToString(data)
}

// requestWithFlash builds an *http.Request carrying the given flash cookies.
func requestWithFlash(t *testing.T, errors, old any) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if errors != nil {
		r.AddCookie(&http.Cookie{Name: flashErrorsCookie, Value: encodeFlash(t, errors)})
	}
	if old != nil {
		r.AddCookie(&http.Cookie{Name: flashInputCookie, Value: encodeFlash(t, old)})
	}
	return r
}

// --- readFlashCookie ---

func TestReadFlashCookie_ReturnsDecodedValue(t *testing.T) {
	errors := map[string]any{
		"email": "The email field is required.",
		"name":  "The name field is required.",
	}
	r := requestWithFlash(t, errors, nil)

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
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	result, ok := readFlashCookie(r, flashErrorsCookie)
	if ok {
		t.Errorf("expected ok to be false for missing cookie, got result: %v", result)
	}
}

func TestReadFlashCookie_EmptyValue(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: flashErrorsCookie, Value: ""})

	result, ok := readFlashCookie(r, flashErrorsCookie)
	if ok {
		t.Errorf("expected ok to be false for empty cookie, got result: %v", result)
	}
}

func TestReadFlashCookie_InvalidBase64(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: flashErrorsCookie, Value: "%%%not-base64%%%"})

	result, ok := readFlashCookie(r, flashErrorsCookie)
	if ok {
		t.Errorf("expected ok to be false for invalid base64, got result: %v", result)
	}
}

func TestReadFlashCookie_InvalidJSON(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	// Valid base64 but not valid JSON
	r.AddCookie(&http.Cookie{
		Name:  flashErrorsCookie,
		Value: base64.URLEncoding.EncodeToString([]byte("not json")),
	})

	result, ok := readFlashCookie(r, flashErrorsCookie)
	if ok {
		t.Errorf("expected ok to be false for invalid JSON, got result: %v", result)
	}
}

func TestReadFlashCookie_StringValue(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: flashErrorsCookie, Value: encodeFlash(t, "simple string")})

	result, ok := readFlashCookie(r, flashErrorsCookie)
	if !ok {
		t.Fatal("expected ok to be true")
	}
	if result != "simple string" {
		t.Errorf("expected 'simple string', got %v", result)
	}
}

func TestReadFlashCookie_ArrayValue(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: flashErrorsCookie, Value: encodeFlash(t, []string{"err1", "err2"})})

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
	errors := map[string]any{"email": "required"}
	r := requestWithFlash(t, errors, nil)
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
	old := map[string]any{"name": "Ali", "email": "ali@example.com"}
	r := requestWithFlash(t, nil, old)
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
	errors := map[string]any{"email": "invalid format"}
	old := map[string]any{"email": "bad-email"}
	r := requestWithFlash(t, errors, old)
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
	errors := map[string]any{"email": "flash error"}
	r := requestWithFlash(t, errors, nil)
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
	r := httptest.NewRequest(http.MethodGet, "/", nil)
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
	errors := map[string]any{"field": "error"}
	r := requestWithFlash(t, errors, nil)
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
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	props := Props{}

	applyFlashData(w, r, props)

	cookies := w.Result().Cookies()
	if len(cookies) != 0 {
		t.Errorf("expected no Set-Cookie headers when no flash data, got %d", len(cookies))
	}
}

func TestApplyFlashData_SecondReadHasNoFlashData(t *testing.T) {
	errors := map[string]any{"field": "error"}
	old := map[string]any{"field": "value"}
	r := requestWithFlash(t, errors, old)
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
	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
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
	// An empty map is still valid JSON — should be applied
	errors := map[string]any{}
	r := requestWithFlash(t, errors, nil)
	w := httptest.NewRecorder()
	props := Props{}

	applyFlashData(w, r, props)

	if props["errors"] == nil {
		t.Error("expected empty map to be set as errors prop")
	}
}

func TestApplyFlashData_NullJSON(t *testing.T) {
	// JSON null — json.Unmarshal produces nil, which is a valid "any"
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{
		Name:  flashErrorsCookie,
		Value: base64.URLEncoding.EncodeToString([]byte("null")),
	})
	w := httptest.NewRecorder()
	props := Props{}

	applyFlashData(w, r, props)

	// JSON null decodes to nil; readFlashCookie returns (nil, true)
	// applyFlashData should set props["errors"] = nil
	if _, ok := props["errors"]; !ok {
		t.Error("expected errors key to be set even for JSON null")
	}
}

func TestReadFlashCookie_NestedData(t *testing.T) {
	nested := map[string]any{
		"email": []any{"required", "must be valid"},
		"address": map[string]any{
			"city": "required",
		},
	}
	r := requestWithFlash(t, nested, nil)

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
