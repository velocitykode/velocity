package testing

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
		// Note: This would fail without a real DB, so we test Make instead
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
		// This would require DB, so just test the panic
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
