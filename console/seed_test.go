package console

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/velocitykode/prism"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/orm/seed"
)

type seedTestSeeder struct {
	name string
	run  func(ctx context.Context, db *orm.Manager) error
}

func (s *seedTestSeeder) Name() string { return s.name }
func (s *seedTestSeeder) Run(ctx context.Context, db *orm.Manager) error {
	if s.run == nil {
		return nil
	}
	return s.run(ctx, db)
}

func recordingSeeder(name string, order *[]string) *seedTestSeeder {
	return &seedTestSeeder{name: name, run: func(context.Context, *orm.Manager) error {
		*order = append(*order, name)
		return nil
	}}
}

// captureSeedOutput runs fn with prism writing into a buffer and returns what
// it printed.
func captureSeedOutput(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prism.SetWriter(&buf)
	defer prism.SetWriter(nil)
	fn()
	return buf.String()
}

func TestSeed_NilDBWarnsAndSkips(t *testing.T) {
	var order []string
	var err error
	out := captureSeedOutput(t, func() {
		err = Seed(context.Background(), nil, []seed.Seeder{recordingSeeder("role", &order)})
	})
	if err != nil {
		t.Fatalf("Seed(nil db) = %v, want nil", err)
	}
	if len(order) != 0 {
		t.Errorf("seeders ran without a database: %v", order)
	}
	if !strings.Contains(out, "skipping seeding") {
		t.Errorf("output %q does not warn about the missing database", out)
	}
}

func TestSeed_NoSeedersWarns(t *testing.T) {
	db := newMigrateTestManager(t)
	var err error
	out := captureSeedOutput(t, func() {
		err = Seed(context.Background(), db, nil)
	})
	if err != nil {
		t.Fatalf("Seed(no seeders) = %v, want nil", err)
	}
	if !strings.Contains(out, "No seeders registered") || !strings.Contains(out, "vel gen seeder") {
		t.Errorf("output %q should warn and hint at gen seeder", out)
	}
}

func TestSeed_RunsAllInOrder(t *testing.T) {
	db := newMigrateTestManager(t)
	var order []string
	seeders := []seed.Seeder{
		recordingSeeder("region", &order),
		recordingSeeder("role", &order),
		recordingSeeder("user", &order),
	}

	var err error
	out := captureSeedOutput(t, func() {
		err = Seed(context.Background(), db, seeders)
	})
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if strings.Join(order, ",") != "region,role,user" {
		t.Errorf("order = %v, want [region role user]", order)
	}
	for _, want := range []string{"Seeding database...", "region", "role", "user", "Done"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestSeed_OnlyRunsTheNamedSeeder(t *testing.T) {
	db := newMigrateTestManager(t)
	var order []string
	seeders := []seed.Seeder{
		recordingSeeder("region", &order),
		recordingSeeder("role", &order),
	}
	if err := Seed(context.Background(), db, seeders, SeedOptions{Only: "role"}); err != nil {
		t.Fatalf("Seed --only role: %v", err)
	}
	if strings.Join(order, ",") != "role" {
		t.Errorf("order = %v, want [role]", order)
	}
}

func TestSeed_OnlyUnknownNameErrors(t *testing.T) {
	db := newMigrateTestManager(t)
	var order []string
	seeders := []seed.Seeder{recordingSeeder("region", &order), recordingSeeder("role", &order)}

	err := Seed(context.Background(), db, seeders, SeedOptions{Only: "nope"})
	if err == nil {
		t.Fatal("Seed --only nope = nil, want error")
	}
	for _, want := range []string{`"nope"`, "region, role"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should contain %q", err.Error(), want)
		}
	}
	if len(order) != 0 {
		t.Errorf("seeders ran despite the unknown --only name: %v", order)
	}
}

func TestSeed_StopsAtFirstFailure(t *testing.T) {
	db := newMigrateTestManager(t)
	boom := errors.New("boom")
	var order []string
	seeders := []seed.Seeder{
		recordingSeeder("ok", &order),
		&seedTestSeeder{name: "fail", run: func(context.Context, *orm.Manager) error { return boom }},
		recordingSeeder("never", &order),
	}

	err := Seed(context.Background(), db, seeders)
	if !errors.Is(err, boom) {
		t.Fatalf("Seed error = %v, want wrapped boom", err)
	}
	if !strings.HasPrefix(err.Error(), "velocity/console: seeding failed: seed: seeder fail failed:") {
		t.Errorf("error = %q, want console + seed prefixes", err.Error())
	}
	if strings.Join(order, ",") != "ok" {
		t.Errorf("order = %v, want [ok]", order)
	}
}

func TestSeed_CancelledContextStopsRun(t *testing.T) {
	db := newMigrateTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var order []string
	err := Seed(ctx, db, []seed.Seeder{recordingSeeder("role", &order)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Seed error = %v, want context.Canceled", err)
	}
	if len(order) != 0 {
		t.Errorf("seeders ran under a cancelled context: %v", order)
	}
}

func TestSeed_SeederWritesThroughTheManager(t *testing.T) {
	db := newMigrateTestManager(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	insert := &seedTestSeeder{name: "rows", run: func(ctx context.Context, m *orm.Manager) error {
		_, err := m.Exec(ctx, "INSERT INTO "+migrateTestTable+" (name) VALUES (?), (?)", "a", "b")
		return err
	}}
	if err := Seed(context.Background(), db, []seed.Seeder{insert}); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	var n int
	if err := db.DB().QueryRow("SELECT COUNT(*) FROM " + migrateTestTable).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("rows = %d, want 2", n)
	}
}

// TestSeed_OnlyWithEmptyRegistryErrors pins the ordering: an explicit
// --only must fail against an empty registry instead of taking the
// "nothing registered" shortcut and reporting success.
func TestSeed_OnlyWithEmptyRegistryErrors(t *testing.T) {
	db := newMigrateTestManager(t)
	err := Seed(context.Background(), db, nil, SeedOptions{Only: "role"})
	if err == nil {
		t.Fatal("Seed --only role with no seeders = nil, want error")
	}
	for _, want := range []string{`"role"`, "registered: none"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should contain %q", err.Error(), want)
		}
	}
}
