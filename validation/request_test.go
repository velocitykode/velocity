package validation

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
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

	result, err := Check(r, Rules{
		"name":  {Required(), Min(3)},
		"email": {Required(), Email()},
	})
	if err != nil {
		t.Fatalf("unexpected rule-set error: %v", err)
	}

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

	result, err := Check(r, Rules{
		"name":  {Required(), Min(3)},
		"email": {Required(), Email()},
	})
	if err != nil {
		t.Fatalf("unexpected rule-set error: %v", err)
	}

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

	result, err := Check(r, Rules{
		"name":  {Required()},
		"email": {Required(), Email()},
	})
	if err != nil {
		t.Fatalf("unexpected rule-set error: %v", err)
	}

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

	result, err := Check(r, Rules{
		"title": {Required()},
	}, Messages{
		{Field: "title", Rule: "required"}: "A title is mandatory",
	})
	if err != nil {
		t.Fatalf("unexpected rule-set error: %v", err)
	}

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

	result, err := CheckData(data, Rules{
		"name":  {Required()},
		"email": {Required(), Email()},
	})
	if err != nil {
		t.Fatalf("unexpected rule-set error: %v", err)
	}

	if result.HasErrors() {
		t.Fatalf("expected no errors, got: %v", result.All())
	}
}

func TestCheckData_Invalid(t *testing.T) {
	data := map[string]interface{}{
		"name":  "",
		"email": "bad",
	}

	result, err := CheckData(data, Rules{
		"name":  {Required()},
		"email": {Required(), Email()},
	})
	if err != nil {
		t.Fatalf("unexpected rule-set error: %v", err)
	}

	if !result.HasErrors() {
		t.Fatal("expected errors")
	}
	if result.First("email") == "" {
		t.Error("expected error for email")
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
			name: "pin stripped",
			input: map[string]interface{}{
				"name": "Ali",
				"pin":  "1234",
			},
			wantKeys:   []string{"name"},
			rejectKeys: []string{"pin"},
		},
		{
			name: "cvv stripped",
			input: map[string]interface{}{
				"name": "Ali",
				"cvv":  "123",
			},
			wantKeys:   []string{"name"},
			rejectKeys: []string{"cvv"},
		},
		{
			name: "card_number stripped",
			input: map[string]interface{}{
				"name":        "Ali",
				"card_number": "4111111111111111",
			},
			wantKeys:   []string{"name"},
			rejectKeys: []string{"card_number"},
		},
		{
			name: "ssn stripped",
			input: map[string]interface{}{
				"name": "Ali",
				"ssn":  "123-45-6789",
			},
			wantKeys:   []string{"name"},
			rejectKeys: []string{"ssn"},
		},
		{
			name: "otp stripped",
			input: map[string]interface{}{
				"name": "Ali",
				"otp":  "000000",
			},
			wantKeys:   []string{"name"},
			rejectKeys: []string{"otp"},
		},
		{
			name: "api_key stripped",
			input: map[string]interface{}{
				"name":    "Ali",
				"api_key": "key",
			},
			wantKeys:   []string{"name"},
			rejectKeys: []string{"api_key"},
		},
		{
			name: "private_key stripped",
			input: map[string]interface{}{
				"name":        "Ali",
				"private_key": "key",
			},
			wantKeys:   []string{"name"},
			rejectKeys: []string{"private_key"},
		},
		{
			name: "credentials stripped",
			input: map[string]interface{}{
				"name":        "Ali",
				"credentials": "creds",
			},
			wantKeys:   []string{"name"},
			rejectKeys: []string{"credentials"},
		},
		{
			name: "passcode stripped",
			input: map[string]interface{}{
				"name":     "Ali",
				"passcode": "123456",
			},
			wantKeys:   []string{"name"},
			rejectKeys: []string{"passcode"},
		},
		{
			name: "benign fields retained",
			input: map[string]interface{}{
				"name":  "Ali",
				"email": "a@b.com",
			},
			wantKeys:   []string{"name", "email"},
			rejectKeys: nil,
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
// body over the configured limit returns an *http.MaxBytesError rather
// than treating the truncated prefix as a valid (or invalid) form.
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
	// ParseForm wraps the body Read error; the extraction returns the
	// *http.MaxBytesError itself.
	var mbe *http.MaxBytesError
	if !errors.As(err, &mbe) || error(mbe) != err {
		t.Fatalf("expected the *http.MaxBytesError itself, got %T: %v", err, err)
	}
}

// TestCheckW_OversizedBody_ReturnsMaxBytesError exercises the public
// CheckW path at the production limit: an oversized JSON or form body
// returns the *http.MaxBytesError itself (the error pipeline answers 413)
// and no result, never a field-level error.
func TestCheckW_OversizedBody_ReturnsMaxBytesError(t *testing.T) {
	// Bodies well over DefaultMaxBodyBytes (10 MiB) exercise the
	// production limit, not just the small-limit unit tests above.
	// strings.NewReader emits the body on demand.
	big := strings.Repeat("a", int(DefaultMaxBodyBytes)+1024)
	form := url.Values{}
	form.Set("name", big)
	tests := []struct {
		name        string
		contentType string
		body        string
	}{
		{name: "json", contentType: "application/json", body: `{"name":"` + big + `"}`},
		{name: "form", contentType: "application/x-www-form-urlencoded", body: form.Encode()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			r.Header.Set("Content-Type", tt.contentType)

			result, err := CheckW(httptest.NewRecorder(), r, Rules{"name": {Required()}})
			if result != nil {
				t.Errorf("result = %v, want nil", result.All())
			}
			var mbe *http.MaxBytesError
			if !errors.As(err, &mbe) || error(mbe) != err {
				t.Fatalf("err = %T %v, want the *http.MaxBytesError itself", err, err)
			}
		})
	}
}

// TestExtractRequestDataLimited_BodyErrors asserts the body the check
// cannot use is reported, not turned into an empty map: a malformed JSON
// or form body is a 400 carrying the parse error as its cause, a body over
// the limit is the *http.MaxBytesError itself, and an empty JSON body is
// an empty map.
func TestExtractRequestDataLimited_BodyErrors(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		limit       int64
		wantData    map[string]interface{}
		wantStatus  int              // 0: no HTTPError expected
		wantCause   func(error) bool // checked on the HTTPError's cause
		wantTooBig  bool             // expect the *http.MaxBytesError itself
	}{
		{name: "malformed json", contentType: "application/json", body: `{"email": "a@example.com", "name":`, wantStatus: http.StatusBadRequest, wantCause: isJSONSyntaxError},
		{name: "json with trailing text", contentType: "application/json", body: `{"name":"a"} trailing`, wantStatus: http.StatusBadRequest, wantCause: isJSONSyntaxError},
		{name: "json array", contentType: "application/json", body: `["a"]`, wantStatus: http.StatusBadRequest, wantCause: isJSONTypeError},
		{name: "malformed form", contentType: "application/x-www-form-urlencoded", body: "name=%zz", wantStatus: http.StatusBadRequest, wantCause: isEscapeError},
		{name: "oversized json", contentType: "application/json", body: `{"name":"` + strings.Repeat("a", 100) + `"}`, limit: 16, wantTooBig: true},
		{name: "oversized form", contentType: "application/x-www-form-urlencoded", body: "name=" + strings.Repeat("a", 100), limit: 16, wantTooBig: true},
		{name: "empty json body", contentType: "application/json", body: "", wantData: map[string]interface{}{}},
		{name: "whitespace json body", contentType: "application/json", body: " \n\t", wantData: map[string]interface{}{}},
		{name: "json null", contentType: "application/json", body: "null", wantData: map[string]interface{}{}},
		{name: "valid json", contentType: "application/json", body: `{"name":"a"}`, wantData: map[string]interface{}{"name": "a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limit := tt.limit
			if limit == 0 {
				limit = DefaultMaxBodyBytes
			}
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			r.Header.Set("Content-Type", tt.contentType)

			data, err := ExtractRequestDataLimited(httptest.NewRecorder(), r, limit)

			switch {
			case tt.wantTooBig:
				var mbe *http.MaxBytesError
				if !errors.As(err, &mbe) || error(mbe) != err {
					t.Fatalf("err = %T %v, want the *http.MaxBytesError itself", err, err)
				}
			case tt.wantStatus != 0:
				var he *contract.HTTPError
				if !errors.As(err, &he) {
					t.Fatalf("err = %T %v, want *contract.HTTPError", err, err)
				}
				if he.Status != tt.wantStatus || he.Message != "malformed request body" {
					t.Errorf("HTTPError = %d %q, want %d %q", he.Status, he.Message, tt.wantStatus, "malformed request body")
				}
				if !tt.wantCause(he.Cause) {
					t.Errorf("cause = %T %v, want the parse error", he.Cause, he.Cause)
				}
			default:
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				if !reflect.DeepEqual(data, tt.wantData) {
					t.Errorf("data = %v, want %v", data, tt.wantData)
				}
				return
			}
			if data != nil {
				t.Errorf("data = %v, want nil", data)
			}
		})
	}
}

// TestCheckW_BodyErrors asserts CheckW returns the body errors with no
// result, and still validates an empty JSON body (required fails).
func TestCheckW_BodyErrors(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int // 0: expect a result with a required failure
	}{
		{name: "malformed json", body: `{"name":`, wantStatus: http.StatusBadRequest},
		{name: "empty json body", body: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			r.Header.Set("Content-Type", "application/json")

			result, err := CheckW(httptest.NewRecorder(), r, Rules{"name": {Required()}})
			if tt.wantStatus != 0 {
				if status, _, ok := contract.StatusOf(err); !ok || status != tt.wantStatus {
					t.Fatalf("err = %v (status %d), want status %d", err, status, tt.wantStatus)
				}
				if result != nil {
					t.Errorf("result = %v, want nil", result.All())
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if result.First("name") == "" {
				t.Errorf("errors = %v, want a required failure on name", result.All())
			}
		})
	}
}

func isJSONSyntaxError(err error) bool {
	var se *json.SyntaxError
	return errors.As(err, &se)
}

func isJSONTypeError(err error) bool {
	var te *json.UnmarshalTypeError
	return errors.As(err, &te)
}

func isEscapeError(err error) bool {
	var ee url.EscapeError
	return errors.As(err, &ee)
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

	result, err := Check(r, Rules{
		"name":  {Required(), Min(3)},
		"email": {Required(), Email()},
	})
	if err != nil {
		t.Fatalf("unexpected rule-set error: %v", err)
	}
	if result.HasErrors() {
		t.Fatalf("expected no errors for normal-sized body, got: %v", result.All())
	}
}
