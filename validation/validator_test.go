package validation

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
				"name":  {"required|string"},
				"email": {"required|email"},
			},
			wantError: false,
		},
		{
			name: "missing required field",
			data: map[string]interface{}{
				"name": "John",
			},
			rules: Rules{
				"name":  {"required"},
				"email": {"required"},
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
				"email": {"email"},
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
				"name": {"string|min:3"},
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
				"age": {"numeric|min:18|max:65"},
			},
			wantError: false,
		},
		{
			name: "between validation",
			data: map[string]interface{}{
				"score": 75,
			},
			rules: Rules{
				"score": {"numeric|between:0,100"},
			},
			wantError: false,
		},
		{
			name: "in validation",
			data: map[string]interface{}{
				"role": "admin",
			},
			rules: Rules{
				"role": {"in:admin,user,guest"},
			},
			wantError: false,
		},
		{
			name: "not_in validation",
			data: map[string]interface{}{
				"username": "admin",
			},
			rules: Rules{
				"username": {"not_in:admin,root,system"},
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
				"password": {"required|min:8|confirmed"},
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
				"password": {"confirmed"},
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
				"terms": {"accepted"},
			},
			wantError: false,
		},
		{
			name: "nullable field",
			data: map[string]interface{}{
				"bio": nil,
			},
			rules: Rules{
				"bio": {"nullable|string"},
			},
			wantError: false,
		},
		{
			name: "alpha validation",
			data: map[string]interface{}{
				"name": "JohnDoe",
			},
			rules: Rules{
				"name": {"alpha"},
			},
			wantError: false,
		},
		{
			name: "alpha_num validation",
			data: map[string]interface{}{
				"code": "ABC123",
			},
			rules: Rules{
				"code": {"alpha_num"},
			},
			wantError: false,
		},
		{
			name: "alpha_dash validation",
			data: map[string]interface{}{
				"username": "john_doe-123",
			},
			rules: Rules{
				"username": {"alpha_dash"},
			},
			wantError: false,
		},
		{
			name: "array validation",
			data: map[string]interface{}{
				"tags": []string{"go", "web", "framework"},
			},
			rules: Rules{
				"tags": {"array|min:1|max:5"},
			},
			wantError: false,
		},
		{
			name: "boolean validation",
			data: map[string]interface{}{
				"active": true,
			},
			rules: Rules{
				"active": {"boolean"},
			},
			wantError: false,
		},
		{
			name: "integer validation",
			data: map[string]interface{}{
				"count": 42,
			},
			rules: Rules{
				"count": {"integer"},
			},
			wantError: false,
		},
		{
			name: "size validation",
			data: map[string]interface{}{
				"code": "123456",
			},
			rules: Rules{
				"code": {"size:6"},
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
				"confirm_password": {"same:password"},
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
				"new_email": {"different:old_email"},
			},
			wantError: false,
		},
		{
			name: "url validation",
			data: map[string]interface{}{
				"website": "https://example.com",
			},
			rules: Rules{
				"website": {"url"},
			},
			wantError: false,
		},
		{
			name: "filled validation",
			data: map[string]interface{}{
				"optional": "",
			},
			rules: Rules{
				"optional": {"filled"},
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
		"name":  {"required|string"},
		"email": {"required|email"},
		"age":   {"required|numeric"},
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
		rule      string
		wantError bool
	}{
		{
			name:      "valid email",
			value:     "test@example.com",
			rule:      "email",
			wantError: false,
		},
		{
			name:      "invalid email",
			value:     "not-an-email",
			rule:      "email",
			wantError: true,
		},
		{
			name:      "valid integer",
			value:     42,
			rule:      "integer|min:0|max:100",
			wantError: false,
		},
		{
			name:      "string min length",
			value:     "ab",
			rule:      "string|min:3",
			wantError: true,
		},
	}

	v := NewValidator()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.ValidateValue(tt.value, tt.rule)
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
		"email.required": "Email address is required",
		"email.email":    "Please provide a valid email address",
	})

	data := map[string]interface{}{
		"email": "invalid",
	}

	rules := Rules{
		"email": {"required|email"},
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
// These are not exhaustive — just the assertions that would fail on a
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
			rules:     Rules{"email": {"required"}},
			field:     "email",
			mustMatch: []string{"email", "required"},
		},
		{
			name:      "email says invalid",
			data:      map[string]interface{}{"email": "nope"},
			rules:     Rules{"email": {"email"}},
			field:     "email",
			mustMatch: []string{"email"},
		},
		{
			name:      "min reports the threshold",
			data:      map[string]interface{}{"name": "Jo"},
			rules:     Rules{"name": {"min:3"}},
			field:     "name",
			mustMatch: []string{"name", "3"},
		},
		{
			name:      "confirmed names the field",
			data:      map[string]interface{}{"password": "secret", "password_confirmation": "nope"},
			rules:     Rules{"password": {"confirmed"}},
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

	// Register a custom rule on the validator instance
	v.RegisterRule("uppercase", func(field string, value interface{}, params []string, data map[string]interface{}) error {
		str, ok := value.(string)
		if !ok {
			return nil
		}
		if str != strings.ToUpper(str) {
			return fmt.Errorf("%s must be uppercase", field)
		}
		return nil
	})

	// Test the custom rule
	data := map[string]interface{}{
		"code": "ABC",
	}

	rules := Rules{
		"code": {"uppercase"},
	}

	_, err := v.Validate(data, rules)
	if err != nil {
		t.Errorf("Unexpected error for uppercase value: %v", err)
	}

	// Test with lowercase (should fail)
	data["code"] = "abc"
	_, err = v.Validate(data, rules)
	if err == nil {
		t.Error("Expected error for lowercase value")
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
				"phone": {"required_if:contact_method,phone"},
			},
			wantError: false,
		},
		{
			name: "required_if fails when condition met and value missing",
			data: map[string]interface{}{
				"contact_method": "phone",
			},
			rules: Rules{
				"phone": {"required_if:contact_method,phone"},
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
				"phone": {"required_if:contact_method,phone"},
			},
			wantError: false,
		},
		{
			name: "required_unless passes when other field has exempt value",
			data: map[string]interface{}{
				"role": "admin",
			},
			rules: Rules{
				"reason": {"required_unless:role,admin"},
			},
			wantError: false,
		},
		{
			name: "required_unless fails when other field does not have exempt value",
			data: map[string]interface{}{
				"role": "user",
			},
			rules: Rules{
				"reason": {"required_unless:role,admin"},
			},
			wantError:   true,
			errorFields: []string{"reason"},
		},
		{
			name: "required_with passes when other field absent",
			data: map[string]interface{}{},
			rules: Rules{
				"city": {"required_with:address"},
			},
			wantError: false,
		},
		{
			name: "required_with fails when other field present and value missing",
			data: map[string]interface{}{
				"address": "123 Main St",
			},
			rules: Rules{
				"city": {"required_with:address"},
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
				"city": {"required_with:address"},
			},
			wantError: false,
		},
		{
			name: "required_without passes when other field is present",
			data: map[string]interface{}{
				"email": "test@example.com",
			},
			rules: Rules{
				"phone": {"required_without:email"},
			},
			wantError: false,
		},
		{
			name: "required_without fails when other field absent and value missing",
			data: map[string]interface{}{},
			rules: Rules{
				"phone": {"required_without:email"},
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
				"phone": {"required_without:email"},
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
				"phone": {"required_if:type,business|min:7"},
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
		"name":     {"required|string|min:3|max:255"},
		"email":    {"required|email"},
		"age":      {"required|integer|min:18|max:100"},
		"password": {"required|string|min:8"},
		"role":     {"required|in:admin,user,guest"},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = v.Validate(data, rules)
	}
}

func BenchmarkParseRules(b *testing.B) {
	ruleString := "required|string|min:3|max:255|alpha_dash"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		parseRules(ruleString)
	}
}
