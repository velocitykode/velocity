package seed

import (
	"context"
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

// --- Test doubles ---

type stubSeeder struct {
	name string
	run  func(ctx context.Context, db *orm.Manager) error
}

func (s *stubSeeder) Name() string { return s.name }
func (s *stubSeeder) Run(ctx context.Context, db *orm.Manager) error {
	if s.run == nil {
		return nil
	}
	return s.run(ctx, db)
}

// recording returns a seeder that appends its name to order when run.
func recording(name string, order *[]string) *stubSeeder {
	return &stubSeeder{name: name, run: func(context.Context, *orm.Manager) error {
		*order = append(*order, name)
		return nil
	}}
}

// --- Helpers ---

func newTestManager(t *testing.T) *orm.Manager {
	t.Helper()
	manager, err := orm.NewManager(orm.ManagerConfig{
		Driver:   "sqlite",
		Database: ":memory:",
	})
	if err != nil {
		t.Fatalf("failed to create ORM manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	return manager
}

func mustNewRunner(t *testing.T, manager *orm.Manager) *Runner {
	t.Helper()
	runner, err := NewRunner(manager)
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}
	return runner
}

func runMigrations(t *testing.T, manager *orm.Manager) {
	t.Helper()
	migrator := migrate.NewMigrator(manager.DB(), manager.DriverName())
	if err := migrator.Up(); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}
}

func countRows(t *testing.T, manager *orm.Manager, table string) int {
	t.Helper()
	var n int
	if err := manager.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// --- Runner construction ---

func TestNewRunner_NilManager(t *testing.T) {
	if _, err := NewRunner(nil); err == nil {
		t.Fatal("NewRunner(nil) = nil error, want error")
	}
}

// --- Runner.Run ---

func TestRunnerRun_ExecutesInOrderAndRecords(t *testing.T) {
	manager := newTestManager(t)
	runner := mustNewRunner(t, manager)

	var order []string
	err := runner.Run(context.Background(),
		recording("region", &order),
		recording("role", &order),
		recording("user", &order),
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := []string{"region", "role", "user"}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Errorf("execution order = %v, want %v", order, want)
	}
	if got := runner.Ran(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Ran() = %v, want %v", got, want)
	}
}

func TestRunnerRun_PassesContextAndManager(t *testing.T) {
	manager := newTestManager(t)
	runner := mustNewRunner(t, manager)

	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "marker")

	var gotCtx context.Context
	var gotDB *orm.Manager
	s := &stubSeeder{name: "observe", run: func(ctx context.Context, db *orm.Manager) error {
		gotCtx, gotDB = ctx, db
		return nil
	}}
	if err := runner.Run(ctx, s); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotCtx == nil || gotCtx.Value(key{}) != "marker" {
		t.Error("seeder did not receive the caller's context")
	}
	if gotDB != manager {
		t.Error("seeder did not receive the runner's manager")
	}
}

func TestRunnerRun_StopsAtFirstFailure(t *testing.T) {
	manager := newTestManager(t)
	runner := mustNewRunner(t, manager)

	boom := errors.New("boom")
	var order []string
	err := runner.Run(context.Background(),
		recording("ok", &order),
		&stubSeeder{name: "fail", run: func(context.Context, *orm.Manager) error { return boom }},
		recording("never", &order),
	)
	if err == nil {
		t.Fatal("Run = nil, want error")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error %v does not wrap the seeder's error", err)
	}
	if want := "seed: seeder fail failed: boom"; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if fmt.Sprint(order) != fmt.Sprint([]string{"ok"}) {
		t.Errorf("executed %v, want only [ok]", order)
	}
	if got := runner.Ran(); len(got) != 1 || got[0] != "ok" {
		t.Errorf("Ran() = %v, want [ok]: a failing seeder must not be recorded", got)
	}
}

func TestRunnerRun_NilSeeder(t *testing.T) {
	manager := newTestManager(t)
	runner := mustNewRunner(t, manager)

	err := runner.Run(context.Background(), nil)
	if err == nil || err.Error() != "seed: seeder cannot be nil" {
		t.Fatalf("Run(nil) error = %v, want %q", err, "seed: seeder cannot be nil")
	}
}

func TestRunnerRun_CancelledContextStopsBetweenSeeders(t *testing.T) {
	manager := newTestManager(t)
	runner := mustNewRunner(t, manager)

	ctx, cancel := context.WithCancel(context.Background())
	var order []string
	err := runner.Run(ctx,
		&stubSeeder{name: "first", run: func(context.Context, *orm.Manager) error {
			order = append(order, "first")
			cancel() // simulate Ctrl-C while the first seeder is running
			return nil
		}},
		recording("second", &order),
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if fmt.Sprint(order) != fmt.Sprint([]string{"first"}) {
		t.Errorf("executed %v, want only [first]", order)
	}
	if got := runner.Ran(); len(got) != 1 || got[0] != "first" {
		t.Errorf("Ran() = %v, want [first]", got)
	}
}

func TestRunnerRun_AlreadyCancelledContextRunsNothing(t *testing.T) {
	manager := newTestManager(t)
	runner := mustNewRunner(t, manager)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var order []string
	err := runner.Run(ctx, recording("first", &order))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if len(order) != 0 {
		t.Errorf("executed %v, want nothing", order)
	}
}

func TestRunnerRun_NoSeeders(t *testing.T) {
	manager := newTestManager(t)
	runner := mustNewRunner(t, manager)
	if err := runner.Run(context.Background()); err != nil {
		t.Fatalf("Run with no seeders: %v", err)
	}
	if len(runner.Ran()) != 0 {
		t.Errorf("Ran() = %v, want empty", runner.Ran())
	}
}

func TestRunnerRan_ReturnsCopy(t *testing.T) {
	manager := newTestManager(t)
	runner := mustNewRunner(t, manager)

	var order []string
	if err := runner.Run(context.Background(), recording("a", &order)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := runner.Ran()
	got[0] = "mutated"
	if again := runner.Ran(); again[0] != "a" {
		t.Errorf("Ran() exposed internal slice: %v", again)
	}
}

// --- Package-level Run ---

func TestRun_NilManager(t *testing.T) {
	if err := Run(context.Background(), nil, &stubSeeder{name: "x"}); err == nil {
		t.Fatal("Run(nil manager) = nil, want error")
	}
}

func TestRun_ComposesFromInsideASeeder(t *testing.T) {
	manager := newTestManager(t)

	var order []string
	root := &stubSeeder{name: "database", run: func(ctx context.Context, db *orm.Manager) error {
		return Run(ctx, db, recording("role", &order), recording("user", &order))
	}}
	if err := Run(context.Background(), manager, root); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fmt.Sprint(order) != fmt.Sprint([]string{"role", "user"}) {
		t.Errorf("nested order = %v, want [role user]", order)
	}
}

// --- Integration: seeders writing through orm/factory ---

func TestIntegration_SeederWithFactory(t *testing.T) {
	manager := newTestManager(t)
	runMigrations(t, manager)

	users := &stubSeeder{name: "user", run: func(ctx context.Context, db *orm.Manager) error {
		f := factory.NewFactory(db, "users", func() map[string]interface{} {
			return map[string]interface{}{
				"name":  factory.F().Name(),
				"email": factory.F().Email(),
			}
		})
		f.Count(5).Create(ctx)
		return nil
	}}

	if err := Run(context.Background(), manager, users); err != nil {
		t.Fatalf("seeding failed: %v", err)
	}
	if got := countRows(t, manager, "users"); got != 5 {
		t.Errorf("users = %d, want 5", got)
	}
}

func TestIntegration_OrderedSeedersAcrossTables(t *testing.T) {
	manager := newTestManager(t)
	runMigrations(t, manager)

	users := &stubSeeder{name: "user", run: func(ctx context.Context, db *orm.Manager) error {
		f := factory.NewFactory(db, "users", func() map[string]interface{} {
			return map[string]interface{}{
				"name":  factory.F().Name(),
				"email": factory.F().Email(),
			}
		})
		f.Count(3).Create(ctx)
		return nil
	}}
	posts := &stubSeeder{name: "post", run: func(ctx context.Context, db *orm.Manager) error {
		f := factory.NewFactory(db, "posts", func() map[string]interface{} {
			return map[string]interface{}{
				"title":   factory.F().Sentence(5),
				"body":    factory.F().Paragraph(1, 3, 10, " "),
				"user_id": 1,
			}
		})
		f.Count(10).Create(ctx)
		return nil
	}}

	if err := Run(context.Background(), manager, users, posts); err != nil {
		t.Fatalf("seeding failed: %v", err)
	}
	if got := countRows(t, manager, "users"); got != 3 {
		t.Errorf("users = %d, want 3", got)
	}
	if got := countRows(t, manager, "posts"); got != 10 {
		t.Errorf("posts = %d, want 10", got)
	}
}

func TestIntegration_SeederWithSequence(t *testing.T) {
	manager := newTestManager(t)
	runMigrations(t, manager)

	seq := &stubSeeder{name: "sequence", run: func(ctx context.Context, db *orm.Manager) error {
		f := factory.NewFactory(db, "users", func() map[string]interface{} {
			return map[string]interface{}{
				"name":  "User",
				"email": "default@test.com",
			}
		})
		f.Count(3).Sequence("email", func(i int) interface{} {
			return fmt.Sprintf("user%d@test.com", i)
		}).Create(ctx)
		return nil
	}}
	if err := Run(context.Background(), manager, seq); err != nil {
		t.Fatalf("seeding failed: %v", err)
	}

	rows, err := manager.DB().Query("SELECT email FROM users ORDER BY id")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	expected := []string{"user1@test.com", "user2@test.com", "user3@test.com"}
	i := 0
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if i < len(expected) && email != expected[i] {
			t.Errorf("row %d: email = %q, want %q", i, email, expected[i])
		}
		i++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if i != 3 {
		t.Errorf("rows = %d, want 3", i)
	}
}
