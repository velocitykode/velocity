package seed

import (
	"errors"
	"fmt"
	"testing"

	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/orm/factory"
	"github.com/velocitykode/velocity/orm/migrate"
)

func init() {
	migrate.Register(&migrate.Migration{
		Version: "20260101000000",
		Up: func(m *migrate.Migrator) error {
			return m.CreateTable("users", func(t *migrate.TableBuilder) {
				t.ID()
				t.String("name")
				t.String("email")
				t.Timestamps()
			})
		},
		Down: func(m *migrate.Migrator) error {
			return m.DropTable("users")
		},
	})

	migrate.Register(&migrate.Migration{
		Version: "20260101000001",
		Up: func(m *migrate.Migrator) error {
			return m.CreateTable("posts", func(t *migrate.TableBuilder) {
				t.ID()
				t.String("title")
				t.Text("body")
				t.Integer("user_id")
				t.Timestamps()
			})
		},
		Down: func(m *migrate.Migrator) error {
			return m.DropTable("posts")
		},
	})
}

// --- Mock seeders ---

type mockSeeder struct {
	name    string
	runFunc func(*orm.Manager) error
}

func (s *mockSeeder) Name() string                    { return s.name }
func (s *mockSeeder) Run(manager *orm.Manager) error { return s.runFunc(manager) }

func newMockSeeder(name string) *mockSeeder {
	return &mockSeeder{
		name:    name,
		runFunc: func(m *orm.Manager) error { return nil },
	}
}

// --- Test helpers ---

func newTestManager(t *testing.T) *orm.Manager {
	t.Helper()
	manager, err := orm.NewManager(orm.ManagerConfig{
		Driver:   "sqlite",
		Database: ":memory:",
	})
	if err != nil {
		t.Fatalf("failed to create ORM manager: %v", err)
	}
	return manager
}

func runMigrations(t *testing.T, manager *orm.Manager) {
	t.Helper()
	migrator := migrate.NewMigrator(manager.DB(), manager.DriverName())
	if err := migrator.Up(); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}
}

// --- Registry tests ---

func TestRegister(t *testing.T) {
	Reset()
	defer Reset()

	Register(newMockSeeder("TestSeeder"))

	all := All()
	if len(all) != 1 {
		t.Fatalf("expected 1 seeder, got %d", len(all))
	}
	if all[0].Name() != "TestSeeder" {
		t.Errorf("expected name %q, got %q", "TestSeeder", all[0].Name())
	}
}

func TestRegisterMultiple(t *testing.T) {
	Reset()
	defer Reset()

	Register(newMockSeeder("First"))
	Register(newMockSeeder("Second"))
	Register(newMockSeeder("Third"))

	all := All()
	if len(all) != 3 {
		t.Fatalf("expected 3 seeders, got %d", len(all))
	}

	// Verify registration order preserved
	expected := []string{"First", "Second", "Third"}
	for i, s := range all {
		if s.Name() != expected[i] {
			t.Errorf("index %d: expected %q, got %q", i, expected[i], s.Name())
		}
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	Reset()
	defer Reset()

	Register(newMockSeeder("Dup"))

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for duplicate registration")
		}
	}()
	Register(newMockSeeder("Dup"))
}

func TestRegisterNilPanics(t *testing.T) {
	Reset()
	defer Reset()

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil seeder")
		}
	}()
	Register(nil)
}

func TestRegisterEmptyNamePanics(t *testing.T) {
	Reset()
	defer Reset()

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for empty name")
		}
	}()
	Register(newMockSeeder(""))
}

func TestFind(t *testing.T) {
	Reset()
	defer Reset()

	Register(newMockSeeder("FindMe"))

	s, err := Find("FindMe")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Name() != "FindMe" {
		t.Errorf("expected name %q, got %q", "FindMe", s.Name())
	}
}

func TestFindNotFound(t *testing.T) {
	Reset()
	defer Reset()

	_, err := Find("NotHere")
	if err == nil {
		t.Error("expected error for non-existent seeder")
	}
}

func TestReset(t *testing.T) {
	Reset()
	defer Reset()

	Register(newMockSeeder("A"))
	Register(newMockSeeder("B"))

	if len(All()) != 2 {
		t.Fatal("expected 2 seeders before reset")
	}

	Reset()

	if len(All()) != 0 {
		t.Errorf("expected 0 seeders after reset, got %d", len(All()))
	}
}

// --- Runner tests ---

func TestNewRunnerPanicsNilManager(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil manager")
		}
	}()
	NewRunner(nil)
}

func TestRunnerRun(t *testing.T) {
	Reset()
	defer Reset()

	manager := newTestManager(t)
	defer manager.Close()
	runMigrations(t, manager)

	ran := false
	Register(&mockSeeder{
		name: "RunTest",
		runFunc: func(m *orm.Manager) error {
			ran = true
			return nil
		},
	})

	runner := NewRunner(manager)
	if err := runner.Run("RunTest"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !ran {
		t.Error("expected seeder to have run")
	}

	if len(runner.Ran()) != 1 || runner.Ran()[0] != "RunTest" {
		t.Errorf("expected Ran() = [RunTest], got %v", runner.Ran())
	}
}

func TestRunnerRunNotFound(t *testing.T) {
	Reset()
	defer Reset()

	manager := newTestManager(t)
	defer manager.Close()

	runner := NewRunner(manager)
	err := runner.Run("NonExistent")
	if err == nil {
		t.Error("expected error for non-existent seeder")
	}
}

func TestRunnerRunError(t *testing.T) {
	Reset()
	defer Reset()

	manager := newTestManager(t)
	defer manager.Close()

	Register(&mockSeeder{
		name: "Failing",
		runFunc: func(m *orm.Manager) error {
			return errors.New("seeder failed")
		},
	})

	runner := NewRunner(manager)
	err := runner.Run("Failing")
	if err == nil {
		t.Error("expected error from failing seeder")
	}
	if len(runner.Ran()) != 0 {
		t.Errorf("failing seeder should not be in Ran(), got %v", runner.Ran())
	}
}

func TestRunnerRunAll(t *testing.T) {
	Reset()
	defer Reset()

	manager := newTestManager(t)
	defer manager.Close()
	runMigrations(t, manager)

	order := make([]string, 0)
	Register(&mockSeeder{
		name: "First",
		runFunc: func(m *orm.Manager) error {
			order = append(order, "First")
			return nil
		},
	})
	Register(&mockSeeder{
		name: "Second",
		runFunc: func(m *orm.Manager) error {
			order = append(order, "Second")
			return nil
		},
	})

	runner := NewRunner(manager)
	if err := runner.RunAll(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(order) != 2 {
		t.Fatalf("expected 2 seeders to run, got %d", len(order))
	}
	if order[0] != "First" || order[1] != "Second" {
		t.Errorf("expected [First, Second], got %v", order)
	}

	if len(runner.Ran()) != 2 {
		t.Errorf("expected 2 in Ran(), got %d", len(runner.Ran()))
	}
}

func TestRunnerRunAllWithDatabaseSeeder(t *testing.T) {
	Reset()
	defer Reset()

	manager := newTestManager(t)
	defer manager.Close()
	runMigrations(t, manager)

	// Register individual seeders
	userRan := false
	Register(&mockSeeder{
		name: "UserSeeder",
		runFunc: func(m *orm.Manager) error {
			userRan = true
			return nil
		},
	})

	postRan := false
	Register(&mockSeeder{
		name: "PostSeeder",
		runFunc: func(m *orm.Manager) error {
			postRan = true
			return nil
		},
	})

	// Register DatabaseSeeder that orchestrates others
	Register(&mockSeeder{
		name: "DatabaseSeeder",
		runFunc: func(m *orm.Manager) error {
			r := NewRunner(m)
			return r.Call("UserSeeder", "PostSeeder")
		},
	})

	runner := NewRunner(manager)
	if err := runner.RunAll(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !userRan {
		t.Error("expected UserSeeder to have run")
	}
	if !postRan {
		t.Error("expected PostSeeder to have run")
	}

	// RunAll only records DatabaseSeeder at top level
	ran := runner.Ran()
	if len(ran) != 1 || ran[0] != "DatabaseSeeder" {
		t.Errorf("expected Ran() = [DatabaseSeeder], got %v", ran)
	}
}

func TestRunnerCall(t *testing.T) {
	Reset()
	defer Reset()

	manager := newTestManager(t)
	defer manager.Close()
	runMigrations(t, manager)

	order := make([]string, 0)
	Register(&mockSeeder{
		name:    "A",
		runFunc: func(m *orm.Manager) error { order = append(order, "A"); return nil },
	})
	Register(&mockSeeder{
		name:    "B",
		runFunc: func(m *orm.Manager) error { order = append(order, "B"); return nil },
	})
	Register(&mockSeeder{
		name:    "C",
		runFunc: func(m *orm.Manager) error { order = append(order, "C"); return nil },
	})

	runner := NewRunner(manager)
	if err := runner.Call("C", "A", "B"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(order) != 3 {
		t.Fatalf("expected 3, got %d", len(order))
	}
	// Call executes in the order specified, not registration order
	expected := []string{"C", "A", "B"}
	for i, name := range expected {
		if order[i] != name {
			t.Errorf("index %d: expected %q, got %q", i, name, order[i])
		}
	}
}

func TestRunnerCallStopsOnError(t *testing.T) {
	Reset()
	defer Reset()

	manager := newTestManager(t)
	defer manager.Close()

	Register(&mockSeeder{
		name:    "OK",
		runFunc: func(m *orm.Manager) error { return nil },
	})
	Register(&mockSeeder{
		name:    "Fail",
		runFunc: func(m *orm.Manager) error { return errors.New("boom") },
	})
	Register(&mockSeeder{
		name:    "NeverReached",
		runFunc: func(m *orm.Manager) error { return nil },
	})

	runner := NewRunner(manager)
	err := runner.Call("OK", "Fail", "NeverReached")
	if err == nil {
		t.Error("expected error")
	}

	ran := runner.Ran()
	if len(ran) != 1 || ran[0] != "OK" {
		t.Errorf("expected only OK in Ran(), got %v", ran)
	}
}

// --- Convenience function tests ---

func TestSeed(t *testing.T) {
	Reset()
	defer Reset()

	manager := newTestManager(t)
	defer manager.Close()
	runMigrations(t, manager)

	ran := false
	Register(&mockSeeder{
		name:    "OnlySeeder",
		runFunc: func(m *orm.Manager) error { ran = true; return nil },
	})

	if err := Seed(manager); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !ran {
		t.Error("expected seeder to run via Seed()")
	}
}

func TestSeedOne(t *testing.T) {
	Reset()
	defer Reset()

	manager := newTestManager(t)
	defer manager.Close()
	runMigrations(t, manager)

	ran := false
	Register(&mockSeeder{
		name:    "Target",
		runFunc: func(m *orm.Manager) error { ran = true; return nil },
	})
	Register(&mockSeeder{
		name:    "Other",
		runFunc: func(m *orm.Manager) error { t.Error("Other should not run"); return nil },
	})

	if err := SeedOne(manager, "Target"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !ran {
		t.Error("expected Target seeder to run")
	}
}

// --- Integration test: seeders with factories and real database ---

func TestIntegrationSeederWithFactory(t *testing.T) {
	Reset()
	defer Reset()

	manager := newTestManager(t)
	defer manager.Close()
	runMigrations(t, manager)

	// Register a seeder that uses the factory package
	Register(&mockSeeder{
		name: "UserSeeder",
		runFunc: func(m *orm.Manager) error {
			f := factory.NewFactory(m, "users", func() map[string]interface{} {
				return map[string]interface{}{
					"name":  factory.F().Name(),
					"email": factory.F().Email(),
				}
			})
			f.Count(5).Create()
			return nil
		},
	})

	if err := Seed(manager); err != nil {
		t.Fatalf("seeding failed: %v", err)
	}

	// Verify data was seeded
	var count int
	err := manager.DB().QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	if err != nil {
		t.Fatalf("failed to query users: %v", err)
	}
	if count != 5 {
		t.Errorf("expected 5 users, got %d", count)
	}
}

func TestIntegrationDatabaseSeederWithFactories(t *testing.T) {
	Reset()
	defer Reset()

	manager := newTestManager(t)
	defer manager.Close()
	runMigrations(t, manager)

	Register(&mockSeeder{
		name: "UserSeeder",
		runFunc: func(m *orm.Manager) error {
			f := factory.NewFactory(m, "users", func() map[string]interface{} {
				return map[string]interface{}{
					"name":  factory.F().Name(),
					"email": factory.F().Email(),
				}
			})
			f.Count(3).Create()
			return nil
		},
	})

	Register(&mockSeeder{
		name: "PostSeeder",
		runFunc: func(m *orm.Manager) error {
			f := factory.NewFactory(m, "posts", func() map[string]interface{} {
				return map[string]interface{}{
					"title":   factory.F().Sentence(5),
					"body":    factory.F().Paragraph(1, 3, 10, " "),
					"user_id": 1,
				}
			})
			f.Count(10).Create()
			return nil
		},
	})

	Register(&mockSeeder{
		name: "DatabaseSeeder",
		runFunc: func(m *orm.Manager) error {
			return NewRunner(m).Call("UserSeeder", "PostSeeder")
		},
	})

	if err := Seed(manager); err != nil {
		t.Fatalf("seeding failed: %v", err)
	}

	// Verify users
	var userCount int
	if err := manager.DB().QueryRow("SELECT COUNT(*) FROM users").Scan(&userCount); err != nil {
		t.Fatalf("failed to query users: %v", err)
	}
	if userCount != 3 {
		t.Errorf("expected 3 users, got %d", userCount)
	}

	// Verify posts
	var postCount int
	if err := manager.DB().QueryRow("SELECT COUNT(*) FROM posts").Scan(&postCount); err != nil {
		t.Fatalf("failed to query posts: %v", err)
	}
	if postCount != 10 {
		t.Errorf("expected 10 posts, got %d", postCount)
	}

	t.Logf("DatabaseSeeder integration: %d users, %d posts seeded", userCount, postCount)
}

func TestIntegrationSeederWithSequence(t *testing.T) {
	Reset()
	defer Reset()

	manager := newTestManager(t)
	defer manager.Close()
	runMigrations(t, manager)

	Register(&mockSeeder{
		name: "SeqSeeder",
		runFunc: func(m *orm.Manager) error {
			f := factory.NewFactory(m, "users", func() map[string]interface{} {
				return map[string]interface{}{
					"name":  "User",
					"email": "default@test.com",
				}
			})
			f.Count(3).Sequence("email", func(i int) interface{} {
				return fmt.Sprintf("user%d@test.com", i)
			}).Create()
			return nil
		},
	})

	if err := Seed(manager); err != nil {
		t.Fatalf("seeding failed: %v", err)
	}

	rows, err := manager.DB().Query("SELECT email FROM users ORDER BY id")
	if err != nil {
		t.Fatalf("failed to query: %v", err)
	}
	defer rows.Close()

	expected := []string{"user1@test.com", "user2@test.com", "user3@test.com"}
	i := 0
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			t.Fatalf("scan error: %v", err)
		}
		if i < len(expected) && email != expected[i] {
			t.Errorf("row %d: expected %q, got %q", i, expected[i], email)
		}
		i++
	}
	if i != 3 {
		t.Errorf("expected 3 rows, got %d", i)
	}
}
