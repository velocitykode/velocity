package validate

import "testing"

func TestOld_CaseInsensitiveSensitiveFieldFiltering(t *testing.T) {
	tests := []struct {
		name       string
		input      map[string]interface{}
		wantKeys   []string
		rejectKeys []string
	}{
		{
			name: "uppercase PASSWORD is stripped",
			input: map[string]interface{}{
				"name":     "Ali",
				"PASSWORD": "hunter2",
			},
			wantKeys:   []string{"name"},
			rejectKeys: []string{"PASSWORD"},
		},
		{
			name: "mixed case Password is stripped",
			input: map[string]interface{}{
				"email":    "a@b.com",
				"Password": "hunter2",
			},
			wantKeys:   []string{"email"},
			rejectKeys: []string{"Password"},
		},
		{
			name: "uppercase Api_Token is stripped",
			input: map[string]interface{}{
				"username":  "ali",
				"Api_Token": "tok_abc",
			},
			wantKeys:   []string{"username"},
			rejectKeys: []string{"Api_Token"},
		},
		{
			name: "uppercase API_TOKEN is stripped",
			input: map[string]interface{}{
				"username":  "ali",
				"API_TOKEN": "tok_abc",
			},
			wantKeys:   []string{"username"},
			rejectKeys: []string{"API_TOKEN"},
		},
		{
			name: "uppercase CLIENT_SECRET is stripped",
			input: map[string]interface{}{
				"client_id":     "id_123",
				"CLIENT_SECRET": "sec_xyz",
			},
			wantKeys:   []string{"client_id"},
			rejectKeys: []string{"CLIENT_SECRET"},
		},
		{
			name: "mixed case ClientSecret is stripped",
			input: map[string]interface{}{
				"client_id":    "id_123",
				"ClientSecret": "sec_xyz",
			},
			wantKeys:   []string{"client_id"},
			rejectKeys: []string{"ClientSecret"},
		},
		{
			name: "password_confirmation in mixed case is stripped",
			input: map[string]interface{}{
				"email":                 "a@b.com",
				"Password_Confirmation": "hunter2",
			},
			wantKeys:   []string{"email"},
			rejectKeys: []string{"Password_Confirmation"},
		},
		{
			name: "multiple sensitive fields in mixed cases",
			input: map[string]interface{}{
				"name":          "Ali",
				"email":         "ali@test.com",
				"PASSWORD":      "secret123",
				"Api_Token":     "tok_abc",
				"CLIENT_SECRET": "sec_xyz",
				"refresh_TOKEN": "ref_123",
				"SecretKey":     "key_456",
			},
			wantKeys:   []string{"name", "email"},
			rejectKeys: []string{"PASSWORD", "Api_Token", "CLIENT_SECRET", "refresh_TOKEN", "SecretKey"},
		},
		{
			name: "non-sensitive fields are preserved",
			input: map[string]interface{}{
				"username":    "ali",
				"first_name":  "Ali",
				"description": "A description",
				"age":         "30",
			},
			wantKeys:   []string{"username", "first_name", "description", "age"},
			rejectKeys: nil,
		},
		{
			name:       "empty input returns empty map",
			input:      map[string]interface{}{},
			wantKeys:   nil,
			rejectKeys: nil,
		},
		{
			name:       "nil input returns empty map",
			input:      nil,
			wantKeys:   nil,
			rejectKeys: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &Errors{input: tt.input}
			old := e.Old()

			for _, key := range tt.wantKeys {
				if _, ok := old[key]; !ok {
					t.Errorf("expected key %q to be present in Old() output", key)
				}
			}
			for _, key := range tt.rejectKeys {
				if _, ok := old[key]; ok {
					t.Errorf("expected sensitive key %q to be stripped from Old() output", key)
				}
			}
		})
	}
}

func TestOld_PreservesOriginalValues(t *testing.T) {
	e := &Errors{
		input: map[string]interface{}{
			"name":  "Ali",
			"email": "ali@test.com",
			"age":   30,
			"tags":  []string{"go", "web"},
		},
	}

	old := e.Old()

	if old["name"] != "Ali" {
		t.Errorf("expected name=Ali, got %v", old["name"])
	}
	if old["email"] != "ali@test.com" {
		t.Errorf("expected email=ali@test.com, got %v", old["email"])
	}
	if old["age"] != 30 {
		t.Errorf("expected age=30, got %v", old["age"])
	}
	tags, ok := old["tags"].([]string)
	if !ok || len(tags) != 2 {
		t.Errorf("expected tags to be preserved, got %v", old["tags"])
	}
}

func TestOld_DoesNotMutateOriginalInput(t *testing.T) {
	input := map[string]interface{}{
		"name":     "Ali",
		"password": "secret",
	}

	e := &Errors{input: input}
	_ = e.Old()

	// Original input should still have the password key
	if _, ok := input["password"]; !ok {
		t.Error("Old() must not mutate the original input map")
	}
	if _, ok := input["name"]; !ok {
		t.Error("Old() must not mutate the original input map")
	}
}
