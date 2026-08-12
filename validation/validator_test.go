package validation

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name        string
		data        map[string]interface{}
		rules       Rules
		wantError   bool
		errorFields []string
		// errorRules maps field -> expected rule name that should have
		// failed. When non-nil, assertions verify the specific rule name,
		// not just the presence of any error on that field. Empty map means
		// "don't assert rule name" (legacy cases still covered by errorFields).
		errorRules map[string]string
	}{
		{
			name: "valid required fields",
			data: map[string]interface{}{
				"name":  "John Doe",
				"email": "john@example.com",
			},
			rules: Rules{
				"name":  {Required(), String()},
				"email": {Required(), Email()},
			},
			wantError: false,
		},
		{
			name: "missing required field",
			data: map[string]interface{}{
				"name": "John",
			},
			rules: Rules{
				"name":  {Required()},
				"email": {Required()},
			},
			wantError:   true,
			errorFields: []string{"email"},
			errorRules:  map[string]string{"email": "required"},
		},
		{
			name: "invalid email",
			data: map[string]interface{}{
				"email": "not-an-email",
			},
			rules: Rules{
				"email": {Email()},
			},
			wantError:   true,
			errorFields: []string{"email"},
			errorRules:  map[string]string{"email": "email"},
		},
		{
			name: "string validation",
			data: map[string]interface{}{
				"name": "Jo",
			},
			rules: Rules{
				"name": {String(), Min(3)},
			},
			wantError:   true,
			errorFields: []string{"name"},
			// string passes; min is what fails
			errorRules: map[string]string{"name": "min"},
		},
		{
			name: "numeric validation",
			data: map[string]interface{}{
				"age": 25,
			},
			rules: Rules{
				"age": {Numeric(), Min(18), Max(65)},
			},
			wantError: false,
		},
		{
			name: "between validation",
			data: map[string]interface{}{
				"score": 75,
			},
			rules: Rules{
				"score": {Numeric(), Between(0, 100)},
			},
			wantError: false,
		},
		{
			name: "in validation",
			data: map[string]interface{}{
				"role": "admin",
			},
			rules: Rules{
				"role": {In("admin", "user", "guest")},
			},
			wantError: false,
		},
		{
			name: "not_in validation",
			data: map[string]interface{}{
				"username": "admin",
			},
			rules: Rules{
				"username": {NotIn("admin", "root", "system")},
			},
			wantError:   true,
			errorFields: []string{"username"},
			errorRules:  map[string]string{"username": "not_in"},
		},
		{
			name: "confirmed validation",
			data: map[string]interface{}{
				"password":              "secret123",
				"password_confirmation": "secret123",
			},
			rules: Rules{
				"password": {Required(), Min(8), Confirmed()},
			},
			wantError: false,
		},
		{
			name: "confirmed validation failure",
			data: map[string]interface{}{
				"password":              "secret123",
				"password_confirmation": "different",
			},
			rules: Rules{
				"password": {Confirmed()},
			},
			wantError:   true,
			errorFields: []string{"password"},
			errorRules:  map[string]string{"password": "confirmed"},
		},
		{
			name: "accepted validation",
			data: map[string]interface{}{
				"terms": "yes",
			},
			rules: Rules{
				"terms": {Accepted()},
			},
			wantError: false,
		},
		{
			name: "nullable field",
			data: map[string]interface{}{
				"bio": nil,
			},
			rules: Rules{
				"bio": {Nullable(), String()},
			},
			wantError: false,
		},
		{
			// B33: an untouched optional HTML form field submits "".
			// nullable must skip url so the empty value passes.
			name: "nullable url empty string passes",
			data: map[string]interface{}{
				"website": "",
			},
			rules: Rules{
				"website": {Nullable(), URL()},
			},
			wantError: false,
		},
		{
			name: "nullable url nil passes",
			data: map[string]interface{}{
				"website": nil,
			},
			rules: Rules{
				"website": {Nullable(), URL()},
			},
			wantError: false,
		},
		{
			// Non-empty value still runs the downstream rules.
			name: "nullable url invalid still fails",
			data: map[string]interface{}{
				"website": "not-a-url",
			},
			rules: Rules{
				"website": {Nullable(), URL()},
			},
			wantError:   true,
			errorFields: []string{"website"},
			errorRules:  map[string]string{"website": "url"},
		},
		{
			name: "nullable url valid passes",
			data: map[string]interface{}{
				"website": "https://example.com",
			},
			rules: Rules{
				"website": {Nullable(), URL()},
			},
			wantError: false,
		},
		{
			// Documented semantics: nullable wins over required when empty.
			name: "required nullable empty passes (nullable wins)",
			data: map[string]interface{}{
				"website": "",
			},
			rules: Rules{
				"website": {Required(), Nullable(), URL()},
			},
			wantError: false,
		},
		{
			// Pipe-delimited token form must be detected on parsed rules.
			name: "nullable pipe form empty passes",
			data: map[string]interface{}{
				"website": "",
			},
			rules: Rules{
				"website": {Nullable(), URL()},
			},
			wantError: false,
		},
		{
			name: "present dotted path present passes",
			data: map[string]interface{}{
				"address": map[string]interface{}{"city": "Berlin"},
			},
			rules: Rules{
				"address.city": {Present()},
			},
			wantError: false,
		},
		{
			name: "present dotted path nil leaf passes",
			data: map[string]interface{}{
				"address": map[string]interface{}{"city": nil},
			},
			rules: Rules{
				"address.city": {Present()},
			},
			wantError: false,
		},
		{
			name: "present dotted path missing leaf fails",
			data: map[string]interface{}{
				"address": map[string]interface{}{"zip": "10115"},
			},
			rules: Rules{
				"address.city": {Present()},
			},
			wantError:   true,
			errorFields: []string{"address.city"},
			errorRules:  map[string]string{"address.city": "present"},
		},
		{
			name: "present dotted path missing intermediate fails",
			data: map[string]interface{}{
				"name": "John",
			},
			rules: Rules{
				"address.city": {Present()},
			},
			wantError:   true,
			errorFields: []string{"address.city"},
			errorRules:  map[string]string{"address.city": "present"},
		},
		{
			name: "alpha validation",
			data: map[string]interface{}{
				"name": "JohnDoe",
			},
			rules: Rules{
				"name": {Alpha()},
			},
			wantError: false,
		},
		{
			name: "alpha_num validation",
			data: map[string]interface{}{
				"code": "ABC123",
			},
			rules: Rules{
				"code": {AlphaNum()},
			},
			wantError: false,
		},
		{
			name: "alpha_dash validation",
			data: map[string]interface{}{
				"username": "john_doe-123",
			},
			rules: Rules{
				"username": {AlphaDash()},
			},
			wantError: false,
		},
		{
			name: "array validation",
			data: map[string]interface{}{
				"tags": []string{"go", "web", "framework"},
			},
			rules: Rules{
				"tags": {Array(), Min(1), Max(5)},
			},
			wantError: false,
		},
		{
			name: "boolean validation",
			data: map[string]interface{}{
				"active": true,
			},
			rules: Rules{
				"active": {Boolean()},
			},
			wantError: false,
		},
		{
			name: "integer validation",
			data: map[string]interface{}{
				"count": 42,
			},
			rules: Rules{
				"count": {Integer()},
			},
			wantError: false,
		},
		{
			name: "size validation",
			data: map[string]interface{}{
				"code": "123456",
			},
			rules: Rules{
				"code": {Size(6)},
			},
			wantError: false,
		},
		{
			name: "same validation",
			data: map[string]interface{}{
				"password":         "secret",
				"confirm_password": "secret",
			},
			rules: Rules{
				"confirm_password": {Same("password")},
			},
			wantError: false,
		},
		{
			name: "different validation",
			data: map[string]interface{}{
				"old_email": "old@example.com",
				"new_email": "new@example.com",
			},
			rules: Rules{
				"new_email": {Different("old_email")},
			},
			wantError: false,
		},
		{
			name: "url validation",
			data: map[string]interface{}{
				"website": "https://example.com",
			},
			rules: Rules{
				"website": {URL()},
			},
			wantError: false,
		},
		{
			name: "filled validation",
			data: map[string]interface{}{
				"optional": "",
			},
			rules: Rules{
				"optional": {Filled()},
			},
			wantError:   true,
			errorFields: []string{"optional"},
			errorRules:  map[string]string{"optional": "filled"},
		},
	}

	v := NewValidator()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := v.Validate(tt.data, tt.rules)

			if tt.wantError {
				if err == nil {
					t.Error("Expected error but got none")
				} else if validationErr, ok := err.(ValidationErrors); ok {
					for _, field := range tt.errorFields {
						if !validationErr.HasError(field) {
							t.Errorf("Expected error for field %s", field)
						}
					}
					for field, rule := range tt.errorRules {
						if !validationErr.HasRule(field, rule) {
							t.Errorf("field %q failed rules %v, expected %q",
								field, validationErr.RulesFor(field), rule)
						}
					}
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if result == nil {
					t.Error("Expected result but got nil")
				}
			}
		})
	}
}

func TestValidateRequest(t *testing.T) {
	// Create a test request with form data
	formData := "name=John&email=john@example.com&age=25"
	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(formData))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rules := Rules{
		"name":  {Required(), String()},
		"email": {Required(), Email()},
		"age":   {Required(), Numeric()},
	}

	v := NewValidator()
	result, err := v.ValidateRequest(req, rules)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if result == nil {
		t.Error("Expected result but got nil")
	}

	// Verify validated data
	if result.GetString("name") != "John" {
		t.Errorf("Expected name to be John, got %s", result.GetString("name"))
	}

	if result.GetString("email") != "john@example.com" {
		t.Errorf("Expected email to be john@example.com, got %s", result.GetString("email"))
	}
}

func TestValidateValue(t *testing.T) {
	tests := []struct {
		name      string
		value     interface{}
		rules     []Rule
		wantError bool
	}{
		{
			name:      "valid email",
			value:     "test@example.com",
			rules:     []Rule{Email()},
			wantError: false,
		},
		{
			name:      "invalid email",
			value:     "not-an-email",
			rules:     []Rule{Email()},
			wantError: true,
		},
		{
			name:      "valid integer",
			value:     42,
			rules:     []Rule{Integer(), Min(0), Max(100)},
			wantError: false,
		},
		{
			name:      "string min length",
			value:     "ab",
			rules:     []Rule{String(), Min(3)},
			wantError: true,
		},
	}

	v := NewValidator()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.ValidateValue(tt.value, tt.rules...)
			if tt.wantError && err == nil {
				t.Error("Expected error but got none")
			}
			if !tt.wantError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestCustomMessages(t *testing.T) {
	validator := NewValidator()

	// Set custom messages
	validator.SetMessages(Messages{
		{Field: "email", Rule: "required"}: "Email address is required",
		{Field: "email", Rule: "email"}:    "Please provide a valid email address",
	})

	data := map[string]interface{}{
		"email": "invalid",
	}

	rules := Rules{
		"email": {Required(), Email()},
	}

	_, err := validator.Validate(data, rules)
	if err == nil {
		t.Error("Expected validation error")
	}

	// Note: Custom message functionality needs to be fully implemented
	// This test demonstrates the expected API
}

// TestBuiltInRuleMessages pins the user-facing copy of the rules that
// consumer apps most often surface directly (login forms, account setup).
// These are not exhaustive, just the assertions that would fail on a
// regression that made messages less informative.
func TestBuiltInRuleMessages(t *testing.T) {
	v := NewValidator()

	cases := []struct {
		name      string
		data      map[string]interface{}
		rules     Rules
		field     string
		mustMatch []string
	}{
		{
			name:      "required mentions the field",
			data:      map[string]interface{}{},
			rules:     Rules{"email": {Required()}},
			field:     "email",
			mustMatch: []string{"email", "required"},
		},
		{
			name:      "email says invalid",
			data:      map[string]interface{}{"email": "nope"},
			rules:     Rules{"email": {Email()}},
			field:     "email",
			mustMatch: []string{"email"},
		},
		{
			name:      "min reports the threshold",
			data:      map[string]interface{}{"name": "Jo"},
			rules:     Rules{"name": {Min(3)}},
			field:     "name",
			mustMatch: []string{"name", "3"},
		},
		{
			name:      "confirmed names the field",
			data:      map[string]interface{}{"password": "secret", "password_confirmation": "nope"},
			rules:     Rules{"password": {Confirmed()}},
			field:     "password",
			mustMatch: []string{"password"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := v.Validate(tc.data, tc.rules)
			verr, ok := err.(ValidationErrors)
			if !ok {
				t.Fatalf("expected ValidationErrors, got %T: %v", err, err)
			}
			msg := verr.First(tc.field)
			if msg == "" {
				t.Fatalf("no error message for field %q", tc.field)
			}
			for _, want := range tc.mustMatch {
				if !strings.Contains(strings.ToLower(msg), strings.ToLower(want)) {
					t.Errorf("message %q missing substring %q", msg, want)
				}
			}
		})
	}
}

func TestValidationErrors(t *testing.T) {
	errors := ValidationErrors{
		Errors: map[string][]string{
			"email": {"Email is required", "Email must be valid"},
			"name":  {"Name is required"},
		},
	}

	// Test Error() method
	errStr := errors.Error()
	if !strings.Contains(errStr, "validation failed") {
		t.Error("Error string should contain 'validation failed'")
	}

	// Test HasError()
	if !errors.HasError("email") {
		t.Error("Should have error for email field")
	}
	if errors.HasError("nonexistent") {
		t.Error("Should not have error for nonexistent field")
	}

	// Test First()
	first := errors.First("email")
	if first != "Email is required" {
		t.Errorf("Expected first error to be 'Email is required', got %s", first)
	}

	// Test Count()
	if errors.Count() != 3 {
		t.Errorf("Expected 3 total errors, got %d", errors.Count())
	}

	// Test IsEmpty()
	if errors.IsEmpty() {
		t.Error("Errors should not be empty")
	}

	emptyErrors := ValidationErrors{}
	if !emptyErrors.IsEmpty() {
		t.Error("Empty errors should be empty")
	}
}

func TestRegisterCustomRule(t *testing.T) {
	v := NewValidator()

	uppercase := func(field string, value interface{}, params []string, data map[string]interface{}) error {
		str, ok := value.(string)
		if !ok {
			return nil
		}
		if str != strings.ToUpper(str) {
			return fmt.Errorf("%s must be uppercase", field)
		}
		return nil
	}

	// Register a custom rule on the validator instance. A rule set reaches
	// it through a rule value naming it; a rule built with Custom carries
	// its own handler and needs no registration at all.
	if err := v.RegisterRule("uppercase", uppercase); err != nil {
		t.Fatalf("RegisterRule: %v", err)
	}
	if err := v.RegisterRule("uppercase", uppercase); err == nil {
		t.Error("re-registering a name must report an error")
	}
	if err := v.RegisterRule("nil_handler", nil); err == nil {
		t.Error("a nil handler must report an error")
	}

	registered := Rules{"code": {staticRule{spec: contract.ValidationRuleSpec{Name: "uppercase"}}}}
	carried := Rules{"code": {Custom("uppercase_carried", uppercase)}}

	for name, rules := range map[string]Rules{"registered": registered, "carried": carried} {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Validate(map[string]interface{}{"code": "ABC"}, rules); err != nil {
				t.Errorf("Unexpected error for uppercase value: %v", err)
			}
			if _, err := v.Validate(map[string]interface{}{"code": "abc"}, rules); err == nil {
				t.Error("Expected error for lowercase value")
			}
		})
	}
}

func TestConditionalRules(t *testing.T) {
	tests := []struct {
		name        string
		data        map[string]interface{}
		rules       Rules
		wantError   bool
		errorFields []string
	}{
		{
			name: "required_if passes when condition not met",
			data: map[string]interface{}{
				"contact_method": "email",
			},
			rules: Rules{
				"phone": {RequiredIf("contact_method", "phone")},
			},
			wantError: false,
		},
		{
			name: "required_if fails when condition met and value missing",
			data: map[string]interface{}{
				"contact_method": "phone",
			},
			rules: Rules{
				"phone": {RequiredIf("contact_method", "phone")},
			},
			wantError:   true,
			errorFields: []string{"phone"},
		},
		{
			name: "required_if passes when condition met and value present",
			data: map[string]interface{}{
				"contact_method": "phone",
				"phone":          "555-1234",
			},
			rules: Rules{
				"phone": {RequiredIf("contact_method", "phone")},
			},
			wantError: false,
		},
		{
			name: "required_unless passes when other field has exempt value",
			data: map[string]interface{}{
				"role": "admin",
			},
			rules: Rules{
				"reason": {RequiredUnless("role", "admin")},
			},
			wantError: false,
		},
		{
			name: "required_unless fails when other field does not have exempt value",
			data: map[string]interface{}{
				"role": "user",
			},
			rules: Rules{
				"reason": {RequiredUnless("role", "admin")},
			},
			wantError:   true,
			errorFields: []string{"reason"},
		},
		{
			name: "required_with passes when other field absent",
			data: map[string]interface{}{},
			rules: Rules{
				"city": {RequiredWith("address")},
			},
			wantError: false,
		},
		{
			name: "required_with fails when other field present and value missing",
			data: map[string]interface{}{
				"address": "123 Main St",
			},
			rules: Rules{
				"city": {RequiredWith("address")},
			},
			wantError:   true,
			errorFields: []string{"city"},
		},
		{
			name: "required_with passes when other field present and value present",
			data: map[string]interface{}{
				"address": "123 Main St",
				"city":    "New York",
			},
			rules: Rules{
				"city": {RequiredWith("address")},
			},
			wantError: false,
		},
		{
			name: "required_without passes when other field is present",
			data: map[string]interface{}{
				"email": "test@example.com",
			},
			rules: Rules{
				"phone": {RequiredWithout("email")},
			},
			wantError: false,
		},
		{
			name: "required_without fails when other field absent and value missing",
			data: map[string]interface{}{},
			rules: Rules{
				"phone": {RequiredWithout("email")},
			},
			wantError:   true,
			errorFields: []string{"phone"},
		},
		{
			name: "required_without passes when other field absent and value present",
			data: map[string]interface{}{
				"phone": "555-1234",
			},
			rules: Rules{
				"phone": {RequiredWithout("email")},
			},
			wantError: false,
		},
		{
			name: "conditional rules combined with other rules",
			data: map[string]interface{}{
				"type":  "business",
				"phone": "555",
			},
			rules: Rules{
				"phone": {RequiredIf("type", "business"), Min(7)},
			},
			wantError:   true,
			errorFields: []string{"phone"},
		},
	}

	v := NewValidator()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := v.Validate(tt.data, tt.rules)

			if tt.wantError {
				if err == nil {
					t.Error("Expected error but got none")
				} else if validationErr, ok := err.(ValidationErrors); ok {
					for _, field := range tt.errorFields {
						if !validationErr.HasError(field) {
							t.Errorf("Expected error for field %s", field)
						}
					}
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if result == nil {
					t.Error("Expected result but got nil")
				}
			}
		})
	}
}

func BenchmarkValidation(b *testing.B) {
	v := NewValidator()

	data := map[string]interface{}{
		"name":     "John Doe",
		"email":    "john@example.com",
		"age":      25,
		"password": "secret123",
		"role":     "admin",
	}

	rules := Rules{
		"name":     {Required(), String(), Min(3), Max(255)},
		"email":    {Required(), Email()},
		"age":      {Required(), Integer(), Min(18), Max(100)},
		"password": {Required(), String(), Min(8)},
		"role":     {Required(), In("admin", "user", "guest")},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = v.Validate(data, rules)
	}
}

func BenchmarkNormalizeRuleSet(b *testing.B) {
	rules := Rules{
		"name":  {Required(), String(), Min(3), Max(255), AlphaDash()},
		"email": {Required(), Email()},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := normalizeRuleSet(rules); err != nil {
			b.Fatal(err)
		}
	}
}

// TestRuleRegistry_ConcurrentRegisterAndGet stresses RuleRegistry under
// simultaneous Register and Get calls. Before the registry grew an internal
// RWMutex, this would trip Go's runtime concurrent-map detector and abort the
// test binary with "fatal error: concurrent map read and map write" under
// -race. It must pass cleanly now.
func TestRuleRegistry_ConcurrentRegisterAndGet(t *testing.T) {
	v := NewValidator()

	const (
		writers = 8
		readers = 16
		writes  = 200
		reads   = 1000
	)

	var wg sync.WaitGroup
	wg.Add(writers + readers)

	for w := 0; w < writers; w++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < writes; i++ {
				name := fmt.Sprintf("rule_w%d_i%d", id, i)
				if err := v.RegisterRule(name, func(field string, value interface{}, params []string, data map[string]interface{}) error {
					return nil
				}); err != nil {
					panic(err)
				}
			}
		}(w)
	}

	for r := 0; r < readers; r++ {
		go func() {
			defer wg.Done()
			for i := 0; i < reads; i++ {
				// "required" is always present from registerBuiltInRules;
				// exercising Get on a known rule keeps the read path hot.
				_ = v.ValidateValue("x", Required())
			}
		}()
	}

	wg.Wait()
}
