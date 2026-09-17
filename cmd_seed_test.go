package velocity

import (
	"context"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/chain"
	"github.com/velocitykode/velocity/orm"
)

// stubSeeder is a minimal seed.Seeder for the CLI tests. run may be nil.
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

func recordingSeeder(name string, order *[]string) *stubSeeder {
	return &stubSeeder{name: name, run: func(context.Context, *orm.Manager) error {
		*order = append(*order, name)
		return nil
	}}
}

// seederModule is a chain module that implements the optional SeederModule
// interface, proving module-registered seeders reach the registry.
type seederModule struct{ seeders []*stubSeeder }

func (m *seederModule) Init(*app.Services) error       { return nil }
func (m *seederModule) Start(*app.Services) error      { return nil }
func (m *seederModule) Shutdown(context.Context) error { return nil }
func (m *seederModule) Seeders(r *chain.Seeders) {
	for _, s := range m.seeders {
		r.Add(s)
	}
}

// withSQLiteDB points the test app at an in-memory sqlite database so the
// seed path reaches a real *orm.Manager instead of the nil-DB warning.
func withSQLiteDB() Option {
	return func(a *App) {
		a.config.DB = DBConfig{Connection: "sqlite", Database: ":memory:"}
	}
}

func newSeedTestApp(t *testing.T) *App {
	t.Helper()
	a, err := NewTestApp(withSQLiteDB())
	if err != nil {
		t.Fatalf("NewTestApp(sqlite): %v", err)
	}
	if a.ormManager() == nil {
		t.Fatal("test app has no *orm.Manager; sqlite config did not take")
	}
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	return a
}

func TestParseDBSeedArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    string
		wantErr string
	}{
		{name: "none", args: nil, want: ""},
		{name: "only spaced", args: []string{"--only", "role"}, want: "role"},
		{name: "only equals", args: []string{"--only=role"}, want: "role"},
		{name: "force before only", args: []string{"--force", "--only", "role"}, want: "role"},
		{name: "short force after only", args: []string{"--only", "role", "-f"}, want: "role"},
		{name: "force alone", args: []string{"--force"}, want: ""},
		{name: "unknown flag", args: []string{"--bogus"}, wantErr: "unknown flag: --bogus"},
		{name: "positional", args: []string{"role"}, wantErr: "unexpected argument: role"},
		{name: "only missing value", args: []string{"--only"}, wantErr: "needs a value"},
		{name: "only empty equals", args: []string{"--only="}, wantErr: "needs a value"},
		{name: "only swallows no flag", args: []string{"--only", "--force"}, wantErr: "unknown flag: --force"},
		{name: "force with value", args: []string{"--force=yes"}, wantErr: "unknown flag: --force=yes"},
		{name: "short force with value", args: []string{"-f=1", "--only", "role"}, wantErr: "unknown flag: -f=1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDBSeedArgs(tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseDBSeedArgs(%v) error = %v, want containing %q", tc.args, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDBSeedArgs(%v) unexpected error: %v", tc.args, err)
			}
			if got != tc.want {
				t.Errorf("parseDBSeedArgs(%v) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

func TestParseMigrateFreshArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    bool
		wantErr bool
	}{
		{name: "none", args: nil},
		{name: "seed", args: []string{"--seed"}, want: true},
		{name: "force then seed", args: []string{"--force", "--seed"}, want: true},
		{name: "seed then short force", args: []string{"--seed", "-f"}, want: true},
		{name: "force only", args: []string{"-f"}},
		{name: "unknown flag", args: []string{"--bogus"}, wantErr: true},
		{name: "positional", args: []string{"seed"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseMigrateFreshArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseMigrateFreshArgs(%v) = %v, want error", tc.args, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseMigrateFreshArgs(%v) unexpected error: %v", tc.args, err)
			}
			if got != tc.want {
				t.Errorf("parseMigrateFreshArgs(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

// TestDBSeedCmd_RunsRegisteredSeedersInOrder is the end-to-end path: the
// Seeders chain step populates the registry during Bootstrap, and `vel db
// seed` runs every seeder in registration order against the app's manager.
func TestDBSeedCmd_RunsRegisteredSeedersInOrder(t *testing.T) {
	a := newSeedTestApp(t)

	var order []string
	var gotDB *orm.Manager
	var gotCtx context.Context
	a.Seeders(func(r *chain.Seeders) {
		r.Add(recordingSeeder("region", &order))
		r.Add(&stubSeeder{name: "role", run: func(ctx context.Context, db *orm.Manager) error {
			order = append(order, "role")
			gotCtx, gotDB = ctx, db
			return nil
		}})
	})

	cmd, _ := newCommandRegistry().get("db seed")
	if err := cmd.run(a, nil); err != nil {
		t.Fatalf("db seed: %v", err)
	}
	if strings.Join(order, ",") != "region,role" {
		t.Errorf("order = %v, want [region role]", order)
	}
	if gotDB != a.ormManager() {
		t.Error("seeder did not receive the app's *orm.Manager")
	}
	if gotCtx == nil {
		t.Error("seeder received a nil context")
	}
	if !a.bootstrapped {
		t.Error("db seed did not Bootstrap")
	}
}

func TestDBSeedCmd_OnlyRunsTheNamedSeeder(t *testing.T) {
	a := newSeedTestApp(t)

	var order []string
	a.Seeders(func(r *chain.Seeders) {
		r.Add(recordingSeeder("region", &order), recordingSeeder("role", &order))
	})

	cmd, _ := newCommandRegistry().get("db seed")
	if err := cmd.run(a, []string{"--only=role"}); err != nil {
		t.Fatalf("db seed --only=role: %v", err)
	}
	if strings.Join(order, ",") != "role" {
		t.Errorf("order = %v, want [role]", order)
	}
}

func TestDBSeedCmd_OnlyUnknownSeederErrors(t *testing.T) {
	a := newSeedTestApp(t)

	var order []string
	a.Seeders(func(r *chain.Seeders) { r.Add(recordingSeeder("region", &order)) })

	cmd, _ := newCommandRegistry().get("db seed")
	err := cmd.run(a, []string{"--only", "nope"})
	if err == nil || !strings.Contains(err.Error(), `"nope"`) || !strings.Contains(err.Error(), "region") {
		t.Fatalf("db seed --only nope error = %v, want unknown-seeder error naming the registered ones", err)
	}
	if len(order) != 0 {
		t.Errorf("seeders ran despite the unknown name: %v", order)
	}
}

// TestDBSeedCmd_SeederModuleAutoWires asserts chain modules implementing
// SeederModule contribute seeders, before the app's own Seeders callback.
func TestDBSeedCmd_SeederModuleAutoWires(t *testing.T) {
	a := newSeedTestApp(t)

	var order []string
	a.Modules(func(r *chain.ModuleRegistry) {
		r.Add(&seederModule{seeders: []*stubSeeder{recordingSeeder("module-role", &order)}})
	})
	a.Seeders(func(r *chain.Seeders) { r.Add(recordingSeeder("app-user", &order)) })

	cmd, _ := newCommandRegistry().get("db seed")
	if err := cmd.run(a, nil); err != nil {
		t.Fatalf("db seed: %v", err)
	}
	if strings.Join(order, ",") != "module-role,app-user" {
		t.Errorf("order = %v, want [module-role app-user]", order)
	}
}

// TestDBSeedCmd_NoDatabaseIsANoOp pins the no-DB path: the test app without
// DB_CONNECTION warns and returns nil, matching migrate / db wipe.
func TestDBSeedCmd_NoDatabaseIsANoOp(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	ran := false
	a.Seeders(func(r *chain.Seeders) {
		r.Add(&stubSeeder{name: "role", run: func(context.Context, *orm.Manager) error {
			ran = true
			return nil
		}})
	})

	cmd, _ := newCommandRegistry().get("db seed")
	if err := cmd.run(a, nil); err != nil {
		t.Fatalf("db seed without a database: %v", err)
	}
	if ran {
		t.Error("seeder ran without a database")
	}
}

// TestDBSeedCmd_ProductionGuardWording asserts the refusal names the command
// and explains the seed-specific reason, and fires before Bootstrap.
func TestDBSeedCmd_ProductionGuardWording(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	a.config.Env = "production"

	cmd, _ := newCommandRegistry().get("db seed")
	err = cmd.run(a, []string{"--only", "role"})
	if err == nil {
		t.Fatal("db seed in production returned nil, want refusal")
	}
	for _, want := range []string{`"db seed"`, "writes seed data", "--force"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q missing %q", err.Error(), want)
		}
	}
	if a.bootstrapped {
		t.Error("refusal ran Bootstrap; guard must fire first")
	}
}

// TestMigrateFreshCmd_SeedFlag asserts --seed runs the registered seeders
// after the fresh migration, and that without it nothing is seeded.
func TestMigrateFreshCmd_SeedFlag(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		wantRuns bool
	}{
		{name: "with --seed", args: []string{"--seed"}, wantRuns: true},
		{name: "without --seed", args: nil, wantRuns: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newSeedTestApp(t)

			var order []string
			a.Seeders(func(r *chain.Seeders) { r.Add(recordingSeeder("role", &order)) })

			cmd, _ := newCommandRegistry().get("migrate fresh")
			if err := cmd.run(a, tc.args); err != nil {
				t.Fatalf("migrate fresh %v: %v", tc.args, err)
			}
			if got := len(order) == 1; got != tc.wantRuns {
				t.Errorf("seeders ran = %v (order %v), want %v", got, order, tc.wantRuns)
			}
		})
	}
}

// TestAppSeeders_ChainMethodReturnsApp pins the fluent contract shared by
// every chain step.
func TestAppSeeders_ChainMethodReturnsApp(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	if got := a.Seeders(func(*chain.Seeders) {}); got != a {
		t.Error("Seeders() did not return the same *App for chaining")
	}
}

// TestDBSeedCmd_OnlyWithNothingRegisteredErrors is the end-to-end form of
// the ordering rule: a forgotten kernel.go plus --only must not exit 0.
func TestDBSeedCmd_OnlyWithNothingRegisteredErrors(t *testing.T) {
	a := newSeedTestApp(t)

	cmd, _ := newCommandRegistry().get("db seed")
	err := cmd.run(a, []string{"--only", "role"})
	if err == nil || !strings.Contains(err.Error(), "registered: none") {
		t.Fatalf("db seed --only role with empty registry error = %v, want not-registered error", err)
	}
}
