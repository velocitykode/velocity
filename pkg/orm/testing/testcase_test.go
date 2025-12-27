package testing

import (
	"fmt"
	"os"
	"testing"

	"github.com/velocitykode/velocity/pkg/orm"
	"github.com/velocitykode/velocity/pkg/orm/migrate"
)

// Test migration for TestCase tests
func init() {
	migrate.Register(&migrate.Migration{
		Version: "20251010000001",
		Up: func(m *migrate.Migrator) error {
			return m.CreateTable("test_users", func(t *migrate.TableBuilder) {
				t.ID()
				t.String("name")
				t.String("email").Unique()
			})
		},
		Down: func(m *migrate.Migrator) error {
			return m.DropTable("test_users")
		},
	})
}

// UserFactory for TestCase tests
func UserFactory() *Factory {
	faker := Faker()

	factory := NewFactory("test_users", func() map[string]interface{} {
		return map[string]interface{}{
			"name":  faker.Name(),
			"email": faker.Email(),
		}
	})

	return factory
}

// TestMain initializes test database
func TestMain(m *testing.M) {
	os.Setenv("APP_ENV", "testing")

	// Use in-memory SQLite for tests
	orm.Init("sqlite", map[string]any{
		"database": ":memory:",
	})
	defer orm.Close()

	m.Run()
}

func TestTestCaseLazyRefresh(t *testing.T) {
	tc := NewTestCase(t)
	tc.LazyRefreshDatabase()

	// Use factory to create test data
	user := UserFactory().Create()
	if user == nil {
		t.Fatal("factory returned nil")
	}

	// Verify data exists in database
	var count int
	err := tc.DB().QueryRow("SELECT COUNT(*) FROM test_users").Scan(&count)
	if err != nil {
		t.Fatalf("failed to count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 user, got %d", count)
	}

	// Verify factory data
	userData := user.(map[string]interface{})
	if userData["name"] == "" {
		t.Error("factory should generate name")
	}
	if userData["email"] == "" {
		t.Error("factory should generate email")
	}
}

func TestTestCaseLazyRefreshIsolation(t *testing.T) {
	tc := NewTestCase(t)
	tc.LazyRefreshDatabase()

	// This test should NOT see data from previous test
	// because LazyRefreshDatabase rolls back each test's transaction
	var count int
	err := tc.DB().QueryRow("SELECT COUNT(*) FROM test_users").Scan(&count)
	if err != nil {
		t.Fatalf("failed to count: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 users (transaction isolation), got %d", count)
	}

	// Create multiple users with factory
	UserFactory().Count(5).Create()

	// Verify count
	err = tc.DB().QueryRow("SELECT COUNT(*) FROM test_users").Scan(&count)
	if err != nil {
		t.Fatalf("failed to count: %v", err)
	}
	if count != 5 {
		t.Errorf("expected 5 users, got %d", count)
	}
}

func TestTestCaseRefreshDatabase(t *testing.T) {
	tc := NewTestCase(t)
	tc.RefreshDatabase()

	// Create users with factory
	users := UserFactory().Count(3).Create()
	usersSlice := users.([]map[string]interface{})
	if len(usersSlice) != 3 {
		t.Errorf("expected 3 users from factory, got %d", len(usersSlice))
	}

	// Verify in database
	var count int
	err := tc.DB().QueryRow("SELECT COUNT(*) FROM test_users").Scan(&count)
	if err != nil {
		t.Fatalf("failed to count: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 users in database, got %d", count)
	}
}

func TestTestCaseWithStates(t *testing.T) {
	tc := NewTestCase(t)
	tc.LazyRefreshDatabase()

	// Define admin state
	factory := UserFactory()
	factory.DefineState("admin", map[string]interface{}{
		"name": "Admin User",
	})

	// Create admin user
	admin := factory.State("admin").Create()
	adminData := admin.(map[string]interface{})

	if adminData["name"] != "Admin User" {
		t.Errorf("expected admin name, got %v", adminData["name"])
	}
}

func TestTestCaseWithSequence(t *testing.T) {
	tc := NewTestCase(t)
	tc.LazyRefreshDatabase()

	// Create users with sequential emails
	users := UserFactory().
		Count(3).
		Sequence("email", func(i int) interface{} {
			return fmt.Sprintf("user%d@test.com", i)
		}).
		Create()

	usersSlice := users.([]map[string]interface{})
	if len(usersSlice) != 3 {
		t.Errorf("expected 3 users, got %d", len(usersSlice))
	}

	// Verify sequential emails
	for i, u := range usersSlice {
		expected := fmt.Sprintf("user%d@test.com", i+1)
		if u["email"] != expected {
			t.Errorf("user %d: expected email %s, got %v", i, expected, u["email"])
		}
	}
}
