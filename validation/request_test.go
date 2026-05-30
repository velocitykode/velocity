package validation

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Check() tests
// ---------------------------------------------------------------------------

func TestCheck_FormDataValid(t *testing.T) {
	form := url.Values{}
	form.Set("name", "Alice")
	form.Set("email", "alice@example.com")

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	result := Check(r, Rules{
		"name":  {"required|min:3"},
		"email": {"required|email"},
	})

	if result.HasErrors() {
		t.Fatalf("expected no errors, got: %v", result.All())
	}
}

func TestCheck_FormDataInvalid(t *testing.T) {
	form := url.Values{}
	form.Set("name", "Al")
	form.Set("email", "bad")

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	result := Check(r, Rules{
		"name":  {"required|min:3"},
		"email": {"required|email"},
	})

	if !result.HasErrors() {
		t.Fatal("expected validation errors")
	}
	if result.First("name") == "" {
		t.Error("expected error for name")
	}
	if result.First("email") == "" {
		t.Error("expected error for email")
	}
}

func TestCheck_JSONBody(t *testing.T) {
	body := `{"name":"","email":"not-an-email"}`
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(body)))
	r.Header.Set("Content-Type", "application/json")

	result := Check(r, Rules{
		"name":  {"required"},
		"email": {"required|email"},
	})

	if !result.HasErrors() {
		t.Fatal("expected validation errors")
	}
	if result.First("name") == "" {
		t.Error("expected error for name")
	}
	if result.First("email") == "" {
		t.Error("expected error for email")
	}
}

func TestCheck_CustomMessages(t *testing.T) {
	form := url.Values{}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	result := Check(r, Rules{
		"title": {"required"},
	}, Messages{
		"title.required": "A title is mandatory",
	})

	if !result.HasErrors() {
		t.Fatal("expected errors")
	}
	if got := result.First("title"); got != "A title is mandatory" {
		t.Errorf("expected custom message, got: %s", got)
	}
}

// ---------------------------------------------------------------------------
// CheckData() tests
// ---------------------------------------------------------------------------

func TestCheckData_Valid(t *testing.T) {
	data := map[string]interface{}{
		"name":  "Alice",
		"email": "alice@example.com",
	}

	result := CheckData(data, Rules{
		"name":  {"required"},
		"email": {"required|email"},
	})

	if result.HasErrors() {
		t.Fatalf("expected no errors, got: %v", result.All())
	}
}

func TestCheckData_Invalid(t *testing.T) {
	data := map[string]interface{}{
		"name":  "",
		"email": "bad",
	}

	result := CheckData(data, Rules{
		"name":  {"required"},
		"email": {"required|email"},
	})

	if !result.HasErrors() {
		t.Fatal("expected errors")
	}
	if result.First("email") == "" {
		t.Error("expected error for email")
	}
}

// TestCheck_BothFormsConverge confirms that PipeRules->NewRules path and a
// hand-written slice literal produce identical validation outcomes.
func TestCheck_BothFormsConverge(t *testing.T) {
	form := url.Values{}
	form.Set("name", "Al") // too short
	r1 := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r1.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r2 := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r2.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	slice := Rules{"name": {"required", "min:3"}}
	pipe := NewRules(PipeRules{"name": "required|min:3"})

	a := Check(r1, slice)
	b := Check(r2, pipe)

	if a.HasErrors() != b.HasErrors() {
		t.Fatalf("convergence mismatch: slice=%v pipe=%v", a.HasErrors(), b.HasErrors())
	}
	if a.First("name") != b.First("name") {
		t.Errorf("messages diverge: slice=%q pipe=%q", a.First("name"), b.First("name"))
	}
}

// ---------------------------------------------------------------------------
// Result type method tests
// ---------------------------------------------------------------------------

func TestResult_HasErrors(t *testing.T) {
	noErr := &Result{errors: nil}
	if noErr.HasErrors() {
		t.Error("nil errors map should report no errors")
	}

	empty := &Result{errors: map[string][]string{}}
	if empty.HasErrors() {
		t.Error("empty errors map should report no errors")
	}

	withErr := &Result{errors: map[string][]string{
		"name": {"Name is required"},
	}}
	if !withErr.HasErrors() {
		t.Error("should report errors")
	}
}

func TestResult_First(t *testing.T) {
	r := &Result{errors: map[string][]string{
		"email": {"Email is required", "Email must be valid"},
	}}

	if got := r.First("email"); got != "Email is required" {
		t.Errorf("expected first error, got: %s", got)
	}
	if got := r.First("nonexistent"); got != "" {
		t.Errorf("expected empty string for missing field, got: %s", got)
	}
}

func TestResult_All(t *testing.T) {
	r := &Result{errors: map[string][]string{
		"name":  {"Name is required", "Name too short"},
		"email": {"Invalid email"},
	}}

	all := r.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 fields, got %d", len(all))
	}
	// All() returns only the first error per field
	if all["name"] != "Name is required" {
		t.Errorf("expected first error for name, got: %s", all["name"])
	}
	if all["email"] != "Invalid email" {
		t.Errorf("expected first error for email, got: %s", all["email"])
	}
}

func TestResult_Messages(t *testing.T) {
	errs := map[string][]string{
		"name": {"err1", "err2"},
	}
	r := &Result{errors: errs}

	msgs := r.Messages()
	if len(msgs["name"]) != 2 {
		t.Errorf("expected 2 messages for name, got %d", len(msgs["name"]))
	}
}

func TestResult_Old_CaseInsensitiveSensitiveFields(t *testing.T) {
	tests := []struct {
		name       string
		input      map[string]interface{}
		wantKeys   []string
		rejectKeys []string
	}{
		{
			name: "lowercase sensitive fields",
			input: map[string]interface{}{
				"name":          "Ali",
				"password":      "secret",
				"api_token":     "tok",
				"client_secret": "sec",
			},
			wantKeys:   []string{"name"},
			rejectKeys: []string{"password", "api_token", "client_secret"},
		},
		{
			name: "uppercase sensitive fields",
			input: map[string]interface{}{
				"name":          "Ali",
				"PASSWORD":      "secret",
				"API_TOKEN":     "tok",
				"CLIENT_SECRET": "sec",
			},
			wantKeys:   []string{"name"},
			rejectKeys: []string{"PASSWORD", "API_TOKEN", "CLIENT_SECRET"},
		},
		{
			name: "mixed case sensitive fields",
			input: map[string]interface{}{
				"email":        "a@b.com",
				"Password":     "secret",
				"Api_Token":    "tok",
				"ClientSecret": "sec",
			},
			wantKeys:   []string{"email"},
			rejectKeys: []string{"Password", "Api_Token", "ClientSecret"},
		},
		{
			name:       "empty input",
			input:      map[string]interface{}{},
			wantKeys:   nil,
			rejectKeys: nil,
		},
		{
			name:       "nil input",
			input:      nil,
			wantKeys:   nil,
			rejectKeys: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Result{input: tt.input}
			old := r.Old()

			for _, key := range tt.wantKeys {
				if _, ok := old[key]; !ok {
					t.Errorf("expected key %q in Old() output", key)
				}
			}
			for _, key := range tt.rejectKeys {
				if _, ok := old[key]; ok {
					t.Errorf("expected sensitive key %q to be stripped", key)
				}
			}
		})
	}
}

func TestResult_Old_DoesNotMutateInput(t *testing.T) {
	input := map[string]interface{}{
		"name":     "Ali",
		"password": "secret",
	}

	r := &Result{input: input}
	_ = r.Old()

	if _, ok := input["password"]; !ok {
		t.Error("Old() must not mutate the original input map")
	}
}

// ---------------------------------------------------------------------------
// ExtractRequestData() tests
// ---------------------------------------------------------------------------

func TestExtractRequestData_JSON(t *testing.T) {
	body := `{"name":"Alice","age":30}`
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(body)))
	r.Header.Set("Content-Type", "application/json")

	data := ExtractRequestData(r)

	if data["name"] != "Alice" {
		t.Errorf("expected name=Alice, got %v", data["name"])
	}
	// JSON numbers decode as float64
	if data["age"] != float64(30) {
		t.Errorf("expected age=30, got %v", data["age"])
	}
}

func TestExtractRequestData_Form(t *testing.T) {
	form := url.Values{}
	form.Set("name", "Alice")
	form.Add("tags", "go")
	form.Add("tags", "web")

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	data := ExtractRequestData(r)

	if data["name"] != "Alice" {
		t.Errorf("expected name=Alice, got %v", data["name"])
	}
	// Multiple values should be a slice
	tags, ok := data["tags"].([]string)
	if !ok || len(tags) != 2 {
		t.Errorf("expected tags to be []string with 2 items, got %v", data["tags"])
	}
}

func TestExtractRequestData_EmptyBody(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	data := ExtractRequestData(r)

	if len(data) != 0 {
		t.Errorf("expected empty map, got %v", data)
	}
}

func TestExtractRequestData_JSONRestoresBody(t *testing.T) {
	body := `{"key":"value"}`
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(body)))
	r.Header.Set("Content-Type", "application/json")

	_ = ExtractRequestData(r)

	// Body should be restored for subsequent reads
	var buf bytes.Buffer
	_, err := buf.ReadFrom(r.Body)
	if err != nil {
		t.Fatalf("failed to read restored body: %v", err)
	}
	if buf.String() != body {
		t.Errorf("expected restored body %q, got %q", body, buf.String())
	}
}

// ---------------------------------------------------------------------------
// M-23: http.MaxBytesReader enforcement on JSON + form branches.
//
// Before M-23 the JSON branch used io.LimitReader which silently
// truncated at 10MB and the form branch had no limit at all. The fix
// switches both branches to http.MaxBytesReader (default
// DefaultMaxBodyBytes = 10 MiB), which surfaces an *http.MaxBytesError
// on overrun instead of silently dropping bytes.
// ---------------------------------------------------------------------------

// TestExtractRequestDataLimited_JSON_RejectsOversize asserts that a JSON
// body over the configured limit returns an *http.MaxBytesError so the
// validator can surface a clear field-level error rather than treating
// the truncated prefix as a valid (or invalid) form.
func TestExtractRequestDataLimited_JSON_RejectsOversize(t *testing.T) {
	// Craft a JSON body just over the small limit; the body itself is
	// valid JSON to prove the rejection comes from the size cap, not
	// the parser.
	limit := int64(64)
	big := bytes.Repeat([]byte("a"), 200)
	body := `{"x":"` + string(big) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	data, err := ExtractRequestDataLimited(nil, r, limit)
	if err == nil {
		t.Fatalf("expected *http.MaxBytesError, got nil; data=%v", data)
	}
	var mbe *http.MaxBytesError
	if !errors.As(err, &mbe) {
		t.Fatalf("expected *http.MaxBytesError, got %T: %v", err, err)
	}
}

// TestExtractRequestDataLimited_Form_RejectsOversize is the form-branch
// regression: before M-23, the form branch had no MaxBytesReader at all,
// so an attacker could stream an unbounded application/x-www-form-urlencoded
// body and exhaust memory. After the fix, ParseForm's internal body read
// trips MaxBytesReader and returns an error.
func TestExtractRequestDataLimited_Form_RejectsOversize(t *testing.T) {
	limit := int64(64)
	form := url.Values{}
	form.Set("name", strings.Repeat("x", 200))
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	data, err := ExtractRequestDataLimited(nil, r, limit)
	if err == nil {
		t.Fatalf("expected size error from form branch, got nil; data=%v", data)
	}
	// ParseForm wraps the body Read error; isMaxBytesError walks the
	// chain via errors.As.
	if !isMaxBytesError(err) {
		t.Fatalf("expected wrapped *http.MaxBytesError, got %T: %v", err, err)
	}
}

// TestCheckW_OversizedJSON_SurfacesValidationError exercises the
// public-facing CheckW path: an oversized body should yield a *Result
// with a _body field error, not a silent pass.
func TestCheckW_OversizedJSON_SurfacesValidationError(t *testing.T) {
	// Build a JSON body well over DefaultMaxBodyBytes (10 MiB) so we
	// exercise the production limit, not just the small-limit unit
	// tests above. Using strings.Repeat is cheap because the body is
	// emitted on demand by strings.NewReader.
	big := strings.Repeat("a", int(DefaultMaxBodyBytes)+1024)
	body := `{"x":"` + big + `"}`
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	result := CheckW(w, r, Rules{"x": {"required"}})
	if !result.HasErrors() {
		t.Fatal("expected validation error from oversized body, got no errors")
	}
	if result.First("_body") == "" {
		t.Errorf("expected _body field error, got: %v", result.All())
	}
}

// TestCheckW_OversizedForm_SurfacesValidationError is the form-branch
// counterpart. Before M-23 the form branch was unbounded; we now expect
// a _body validation error.
func TestCheckW_OversizedForm_SurfacesValidationError(t *testing.T) {
	form := url.Values{}
	form.Set("name", strings.Repeat("x", int(DefaultMaxBodyBytes)+1024))
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	w := httptest.NewRecorder()
	result := CheckW(w, r, Rules{"name": {"required"}})
	if !result.HasErrors() {
		t.Fatal("expected validation error from oversized form body, got no errors")
	}
	if result.First("_body") == "" {
		t.Errorf("expected _body field error, got: %v", result.All())
	}
}

// TestCheck_UnderLimit_StillWorks confirms a normal-sized request
// continues to pass through validation untouched after the
// MaxBytesReader rewiring.
func TestCheck_UnderLimit_StillWorks(t *testing.T) {
	form := url.Values{}
	form.Set("name", "Alice")
	form.Set("email", "alice@example.com")
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	result := Check(r, Rules{
		"name":  {"required|min:3"},
		"email": {"required|email"},
	})
	if result.HasErrors() {
		t.Fatalf("expected no errors for normal-sized body, got: %v", result.All())
	}
}
