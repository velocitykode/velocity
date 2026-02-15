package factory

import (
	"reflect"
	"testing"
)

// Mock model for testing
type TestUser struct {
	ID       uint
	Name     string
	Email    string
	Role     string
	IsActive bool
}

func TestModelFactory_CreateOne(t *testing.T) {
	factory := NewModelFactory[TestUser](nil, func() *TestUser {
		return &TestUser{
			Name:     "Default Name",
			Email:    "default@test.com",
			Role:     "user",
			IsActive: true,
		}
	})

	t.Run("creates with defaults", func(t *testing.T) {
		user := factory.makeOne("", nil)

		if user.Name != "Default Name" {
			t.Errorf("expected Name %q, got %q", "Default Name", user.Name)
		}
		if user.Email != "default@test.com" {
			t.Errorf("expected Email %q, got %q", "default@test.com", user.Email)
		}
	})

	t.Run("applies overrides", func(t *testing.T) {
		user := factory.makeOne("", &TestUser{
			Name:  "Custom Name",
			Email: "custom@test.com",
		})

		if user.Name != "Custom Name" {
			t.Errorf("expected Name %q, got %q", "Custom Name", user.Name)
		}
		if user.Email != "custom@test.com" {
			t.Errorf("expected Email %q, got %q", "custom@test.com", user.Email)
		}
		// Non-overridden fields should keep defaults
		if user.Role != "user" {
			t.Errorf("expected Role %q, got %q", "user", user.Role)
		}
	})
}

func TestModelFactory_States(t *testing.T) {
	factory := NewModelFactory[TestUser](nil, func() *TestUser {
		return &TestUser{
			Name:     "Default Name",
			Email:    "default@test.com",
			Role:     "user",
			IsActive: true,
		}
	}).DefineState("admin", func(u *TestUser) {
		u.Role = "admin"
	}).DefineState("inactive", func(u *TestUser) {
		u.IsActive = false
	})

	t.Run("applies state modifier", func(t *testing.T) {
		user := factory.makeOne("admin", nil)

		if user.Role != "admin" {
			t.Errorf("expected Role %q, got %q", "admin", user.Role)
		}
	})

	t.Run("state with overrides", func(t *testing.T) {
		user := factory.makeOne("admin", &TestUser{
			Name: "Admin User",
		})

		if user.Role != "admin" {
			t.Errorf("expected Role %q, got %q", "admin", user.Role)
		}
		if user.Name != "Admin User" {
			t.Errorf("expected Name %q, got %q", "Admin User", user.Name)
		}
	})

	t.Run("panics on unknown state", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic for unknown state")
			}
		}()
		factory.State("nonexistent")
	})
}

func TestModelFactory_Count(t *testing.T) {
	factory := NewModelFactory[TestUser](nil, func() *TestUser {
		return &TestUser{
			Name:  Faker().Name(),
			Email: Faker().Email(),
		}
	})

	t.Run("make returns slice for count > 1", func(t *testing.T) {
		result := factory.Count(3).Make(nil)
		users, ok := result.([]*TestUser)
		if !ok {
			t.Fatalf("expected []*TestUser, got %T", result)
		}
		if len(users) != 3 {
			t.Errorf("expected 3 users, got %d", len(users))
		}
	})

	t.Run("count resets after make", func(t *testing.T) {
		factory.Count(5).Make(nil)
		result := factory.Make(nil)
		if _, ok := result.(*TestUser); !ok {
			t.Errorf("expected *TestUser after reset, got %T", result)
		}
	})

	t.Run("panics on count <= 0", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic for count <= 0")
			}
		}()
		factory.Count(0)
	})
}

func TestModelFactory_MakeOne(t *testing.T) {
	factory := NewModelFactory[TestUser](nil, func() *TestUser {
		return &TestUser{
			Name:  "Test",
			Email: "test@example.com",
		}
	})

	user := factory.MakeOne(nil)
	if user.Name != "Test" {
		t.Errorf("expected Name %q, got %q", "Test", user.Name)
	}
}

func TestModelFactory_CreateMany(t *testing.T) {
	factory := NewModelFactory[TestUser](nil, func() *TestUser {
		return &TestUser{
			Name:  Faker().Name(),
			Email: Faker().Email(),
		}
	})

	t.Run("panics on count <= 0", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic for count <= 0")
			}
		}()
		factory.CreateMany(0, nil)
	})
}

func TestMergeNonZero(t *testing.T) {
	t.Run("merges non-zero string", func(t *testing.T) {
		dst := &TestUser{Name: "Original", Email: "original@test.com"}
		src := &TestUser{Name: "Override"}

		mergeNonZero(dst, src)

		if dst.Name != "Override" {
			t.Errorf("expected Name %q, got %q", "Override", dst.Name)
		}
		if dst.Email != "original@test.com" {
			t.Errorf("expected Email %q (unchanged), got %q", "original@test.com", dst.Email)
		}
	})

	t.Run("merges non-zero bool", func(t *testing.T) {
		dst := &TestUser{IsActive: false}
		src := &TestUser{IsActive: true}

		mergeNonZero(dst, src)

		if dst.IsActive != true {
			t.Error("expected IsActive to be true")
		}
	})

	t.Run("does not merge zero values", func(t *testing.T) {
		dst := &TestUser{Name: "Keep This", Role: "admin"}
		src := &TestUser{Name: "", Role: ""} // zero values

		mergeNonZero(dst, src)

		if dst.Name != "Keep This" {
			t.Errorf("expected Name %q (unchanged), got %q", "Keep This", dst.Name)
		}
		if dst.Role != "admin" {
			t.Errorf("expected Role %q (unchanged), got %q", "admin", dst.Role)
		}
	})
}

func TestIsZeroValue(t *testing.T) {
	tests := []struct {
		name     string
		value    any
		expected bool
	}{
		{"zero string", "", true},
		{"non-zero string", "hello", false},
		{"zero int", 0, true},
		{"non-zero int", 42, false},
		{"zero bool", false, true},
		{"non-zero bool", true, false},
		{"nil pointer", (*string)(nil), true},
		{"nil slice", ([]string)(nil), true},
		{"empty slice", []string{}, false}, // empty != nil
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isZeroValue(reflect.ValueOf(tt.value))
			if result != tt.expected {
				t.Errorf("isZeroValue(%v) = %v, expected %v", tt.value, result, tt.expected)
			}
		})
	}
}

func TestFactory_Make(t *testing.T) {
	f := NewFactory(nil, "users", func() map[string]interface{} {
		return map[string]interface{}{
			"name":  Faker().Name(),
			"email": Faker().Email(),
		}
	})

	t.Run("generates single record", func(t *testing.T) {
		result := f.Make()
		userMap, ok := result.(map[string]interface{})
		if !ok {
			t.Fatal("expected map[string]interface{}")
		}
		if userMap["name"] == nil {
			t.Error("expected name to be set")
		}
		if userMap["email"] == nil {
			t.Error("expected email to be set")
		}
	})

	t.Run("generates multiple records", func(t *testing.T) {
		result := f.Count(3).Make()
		users, ok := result.([]map[string]interface{})
		if !ok {
			t.Fatalf("expected []map[string]interface{}, got %T", result)
		}
		if len(users) != 3 {
			t.Errorf("expected 3 users, got %d", len(users))
		}
	})

	t.Run("resets count after make", func(t *testing.T) {
		f.Count(5).Make()
		result := f.Make()
		if _, ok := result.(map[string]interface{}); !ok {
			t.Errorf("expected single map after reset, got %T", result)
		}
	})

	t.Run("resets state after make", func(t *testing.T) {
		f.DefineState("admin", map[string]interface{}{"role": "admin"})
		f.State("admin").Make()
		// After Make, activeState should be reset; next call should not apply "admin"
		result := f.Make()
		userMap := result.(map[string]interface{})
		if userMap["role"] == "admin" {
			t.Error("state should have been reset after Make()")
		}
	})
}

func TestFactory_State(t *testing.T) {
	f := NewFactory(nil, "users", func() map[string]interface{} {
		return map[string]interface{}{
			"name": "Default",
			"role": "user",
		}
	})
	f.DefineState("admin", map[string]interface{}{
		"role": "admin",
	})

	result := f.State("admin").Make()
	userMap := result.(map[string]interface{})

	if userMap["role"] != "admin" {
		t.Errorf("expected role %q, got %v", "admin", userMap["role"])
	}
}

func TestFactory_Sequence(t *testing.T) {
	f := NewFactory(nil, "users", func() map[string]interface{} {
		return map[string]interface{}{
			"name":  "Default",
			"email": "default@test.com",
		}
	})

	result := f.Count(3).Sequence("email", func(i int) interface{} {
		return "user" + string(rune('0'+i)) + "@test.com"
	}).Make()

	users := result.([]map[string]interface{})
	if len(users) != 3 {
		t.Fatalf("expected 3 users, got %d", len(users))
	}
}

func TestFactory_Overrides(t *testing.T) {
	f := NewFactory(nil, "users", func() map[string]interface{} {
		return map[string]interface{}{
			"name":  "Default",
			"email": "default@test.com",
		}
	})

	result := f.Make(map[string]interface{}{
		"name": "Custom",
	})
	userMap := result.(map[string]interface{})

	if userMap["name"] != "Custom" {
		t.Errorf("expected name %q, got %v", "Custom", userMap["name"])
	}
	if userMap["email"] != "default@test.com" {
		t.Errorf("expected email %q, got %v", "default@test.com", userMap["email"])
	}
}

func TestBuildInsertSQL_Deterministic(t *testing.T) {
	data := map[string]interface{}{
		"name":  "John",
		"email": "john@test.com",
		"age":   30,
	}

	q1, v1 := buildInsertSQL("users", data, "sqlite")
	q2, v2 := buildInsertSQL("users", data, "sqlite")

	if q1 != q2 {
		t.Errorf("queries should be deterministic:\n  %s\n  %s", q1, q2)
	}

	for i := range v1 {
		if v1[i] != v2[i] {
			t.Errorf("values[%d] differ: %v vs %v", i, v1[i], v2[i])
		}
	}
}
