package velocity

import (
	"strings"
	"testing"

	"github.com/velocitykode/velocity/console"
)

// TestCmdRejectsUnknownFlags dispatches each built-in command through the
// registry and asserts that an unrecognised flag (or stray positional) is
// rejected with a descriptive error instead of being silently ignored. This
// is the deliberate behaviour change introduced by this task: a typo now
// errors. The parsers run before Bootstrap, so a bare *App that never
// connects to any service is enough to exercise the rejection path.
func TestCmdRejectsUnknownFlags(t *testing.T) {
	cases := []struct {
		name            string
		command         string
		args            []string
		wantErrContains string
	}{
		// Routes
		{"route:list unknown flag", "route:list", []string{"--bogus"}, "unknown flag: --bogus"},
		// Migrations
		{"migrate unknown flag", "migrate", []string{"--bogus"}, "unknown flag: --bogus"},
		{"migrate:fresh unknown flag", "migrate:fresh", []string{"--bogus"}, "unknown flag: --bogus"},
		{"migrate:rollback unknown flag", "migrate:rollback", []string{"--bogus"}, "unknown flag: --bogus"},
		{"migrate:status unknown flag", "migrate:status", []string{"--bogus"}, "unknown flag: --bogus"},
		// Database
		{"db:wipe unknown flag", "db:wipe", []string{"--bogus"}, "unknown flag: --bogus"},
		// Cache
		{"cache:clear unknown flag", "cache:clear", []string{"--bogus"}, "unknown flag: --bogus"},
		// Code generation (make:*)
		{"make:handler unknown flag", "make:handler", []string{"User", "--bogus"}, "unknown flag: --bogus"},
		{"make:model unknown flag", "make:model", []string{"User", "--bogus"}, "unknown flag: --bogus"},
		{"make:migration unknown flag", "make:migration", []string{"create_x", "--bogus"}, "unknown flag: --bogus"},
		{"make:middleware unknown flag", "make:middleware", []string{"Auth", "--bogus"}, "unknown flag: --bogus"},
		{"make:event unknown flag", "make:event", []string{"Ev", "--bogus"}, "unknown flag: --bogus"},
		{"make:listener unknown flag", "make:listener", []string{"Ln", "--bogus"}, "unknown flag: --bogus"},
		{"make:job unknown flag", "make:job", []string{"Jb", "--bogus"}, "unknown flag: --bogus"},
		{"make:mail unknown flag", "make:mail", []string{"Ml", "--bogus"}, "unknown flag: --bogus"},
		{"make:notification unknown flag", "make:notification", []string{"Nt", "--bogus"}, "unknown flag: --bogus"},
		{"make:resource unknown flag", "make:resource", []string{"Rs", "--bogus"}, "unknown flag: --bogus"},
		{"make:policy unknown flag", "make:policy", []string{"Pl", "--bogus"}, "unknown flag: --bogus"},
		{"make:provider unknown flag", "make:provider", []string{"Pr", "--bogus"}, "unknown flag: --bogus"},
		{"make:command unknown flag", "make:command", []string{"Cm", "--bogus"}, "unknown flag: --bogus"},
		{"make:grpc:rpc unknown flag", "make:grpc:rpc", []string{"Svc", "Rpc", "--bogus"}, "unknown flag: --bogus"},
		{"make:grpc:service unknown flag", "make:grpc:service", []string{"Svc", "--bogus"}, "unknown flag: --bogus"},
		{"make:grpc:gen unknown flag", "make:grpc:gen", []string{"--bogus"}, "unknown flag: --bogus"},
		// Queue & scheduler
		{"queue:work unknown flag", "queue:work", []string{"--bogus"}, "unknown flag: --bogus"},
		{"schedule:work unknown flag", "schedule:work", []string{"--bogus"}, "unknown flag: --bogus"},
		// Maintenance
		{"down unknown flag", "down", []string{"--bogus"}, "unknown flag: --bogus"},
		{"up unknown flag", "up", []string{"--bogus"}, "unknown flag: --bogus"},
		// Keys
		{"key:generate unknown flag", "key:generate", []string{"--bogus"}, "unknown flag: --bogus"},
		// Server / build
		{"serve unknown flag", "serve", []string{"--bogus"}, "unknown flag: --bogus"},
		{"build unknown flag", "build", []string{"--bogus"}, "unknown flag: --bogus"},
		// Custom command runner
		{"run unknown flag", "run", []string{"--bogus"}, "unknown flag: --bogus"},
		// Help (and its --help / -h aliases)
		{"help unknown flag", "help", []string{"--bogus"}, "unknown flag: --bogus"},
		{"--help unknown flag", "--help", []string{"--bogus"}, "unknown flag: --bogus"},
		{"-h unknown flag", "-h", []string{"--bogus"}, "unknown flag: --bogus"},

		// Stray positionals
		{"make:middleware second positional", "make:middleware", []string{"Auth", "Extra"}, "unexpected argument: Extra"},
		{"make:command second positional", "make:command", []string{"Cm", "Extra"}, "unexpected argument: Extra"},
		{"make:grpc:rpc third positional", "make:grpc:rpc", []string{"Svc", "Rpc", "Extra"}, "unexpected argument: Extra"},

		// migrate:rollback --step validation
		{"rollback step junk", "migrate:rollback", []string{"--step", "junk"}, "integer"},
		{"rollback step=junk", "migrate:rollback", []string{"--step=junk"}, "integer"},
		{"rollback step zero", "migrate:rollback", []string{"--step", "0"}, ">= 1"},
		{"rollback step missing value", "migrate:rollback", []string{"--step"}, "needs a value"},

		// Value flags missing their value
		{"queue:work tries missing value", "queue:work", []string{"--tries"}, "needs a value"},
		{"queue:work tries non-integer", "queue:work", []string{"--tries", "abc"}, "integer"},
		{"down retry non-integer", "down", []string{"--retry", "abc"}, "integer"},
		{"make:migration create missing value", "make:migration", []string{"create_x", "--create"}, "needs a value"},
		{"make:migration table missing value", "make:migration", []string{"create_x", "--table"}, "needs a value"},
		{"make:handler dir missing value", "make:handler", []string{"User", "--dir"}, "needs a value"},

		// An unknown flag must not be swallowed as the preceding flag's value.
		{"queue:work queue then unknown flag", "queue:work", []string{"--queue", "--bogus"}, "unknown flag: --bogus"},
		{"down secret then unknown flag", "down", []string{"--secret", "--bogus"}, "unknown flag: --bogus"},
		{"serve env then unknown flag", "serve", []string{"--env", "--bogus"}, "unknown flag: --bogus"},
		{"build output then unknown flag", "build", []string{"--output", "--bogus"}, "unknown flag: --bogus"},
		{"make:migration create then unknown flag", "make:migration", []string{"create_x", "--create", "--bogus"}, "unknown flag: --bogus"},
		{"make:migration table then unknown flag", "make:migration", []string{"create_x", "--table", "--bogus"}, "unknown flag: --bogus"},
		{"make:handler dir then unknown flag", "make:handler", []string{"User", "--dir", "--bogus"}, "unknown flag: --bogus"},
		{"make:grpc:service package then unknown flag", "make:grpc:service", []string{"Svc", "--package", "--bogus"}, "unknown flag: --bogus"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, err := NewTestApp()
			if err != nil {
				t.Fatalf("NewTestApp() error: %v", err)
			}
			cmd, ok := newCommandRegistry().get(tc.command)
			if !ok {
				t.Fatalf("command %q not registered", tc.command)
			}
			err = cmd.run(a, tc.args)
			if err == nil {
				t.Fatalf("%s %v: expected error, got nil", tc.command, tc.args)
			}
			if !strings.Contains(err.Error(), tc.wantErrContains) {
				t.Errorf("%s %v: error = %q, want containing %q", tc.command, tc.args, err.Error(), tc.wantErrContains)
			}
		})
	}
}

// --- Direct unit tests of the extracted parse functions (happy paths). ---

func TestParseRollbackArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    int
		wantErr bool
	}{
		{name: "default", args: nil, want: 1},
		{name: "long spaced", args: []string{"--step", "3"}, want: 3},
		{name: "short spaced", args: []string{"-s", "3"}, want: 3},
		{name: "equals form", args: []string{"--step=3"}, want: 3},
		{name: "with force before", args: []string{"--force", "--step", "2"}, want: 2},
		{name: "with force after", args: []string{"--step", "2", "-f"}, want: 2},
		{name: "junk value", args: []string{"--step", "junk"}, wantErr: true},
		{name: "equals junk", args: []string{"--step=junk"}, wantErr: true},
		{name: "zero", args: []string{"--step", "0"}, wantErr: true},
		{name: "negative", args: []string{"--step=-4"}, wantErr: true},
		{name: "missing value", args: []string{"--step"}, wantErr: true},
		{name: "unknown flag", args: []string{"--nope"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseRollbackArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseRollbackArgs(%v) = %d, want error", tc.args, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRollbackArgs(%v) unexpected error: %v", tc.args, err)
			}
			if got != tc.want {
				t.Errorf("parseRollbackArgs(%v) = %d, want %d", tc.args, got, tc.want)
			}
		})
	}
}

func TestParseQueueWorkArgs(t *testing.T) {
	got, err := parseQueueWorkArgs([]string{"--queue", "emails", "--tries", "5", "--timeout=30"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := console.QueueWorkOptions{Queue: "emails", Tries: 5, Timeout: 30}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if _, err := parseQueueWorkArgs([]string{"-q", "default"}); err != nil {
		t.Errorf("short -q form errored: %v", err)
	}
}

func TestParseDownArgs(t *testing.T) {
	got, err := parseDownArgs([]string{"--secret", "shh", "--retry=120"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := console.DownOptions{Secret: "shh", RetryAfter: 120}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseServeArgs(t *testing.T) {
	base := console.ServeOptions{Port: "4000", Env: "development", Watch: true}
	got, err := parseServeArgs(base, []string{"--port", "8080", "-e", "staging", "--no-watch", "--tags", "sqlite"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := console.ServeOptions{Port: "8080", Env: "staging", Watch: false, BuildTags: "sqlite"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	// Defaults pass through untouched when no flags are given.
	if got, _ := parseServeArgs(base, nil); got != base {
		t.Errorf("no-flags result = %+v, want base %+v", got, base)
	}
}

func TestParseBuildArgs(t *testing.T) {
	got, err := parseBuildArgs([]string{"-o", "bin/app", "--os", "linux", "--arch", "amd64", "--tags=sqlite"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := console.BuildOptions{Output: "bin/app", OS: "linux", Arch: "amd64", Tags: "sqlite"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseMigrateArgs(t *testing.T) {
	got, err := parseMigrateArgs([]string{"--pretend"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Pretend {
		t.Errorf("Pretend = false, want true")
	}
	if _, err := parseMigrateArgs([]string{"--nope"}); err == nil {
		t.Errorf("expected error for unknown flag")
	}
}

func TestParseMakeHandlerArgs(t *testing.T) {
	got, err := parseMakeHandlerArgs([]string{"--resource", "--api", "--dir", "internal/web"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := console.MakeHandlerOptions{Resource: true, API: true, Dir: "internal/web"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got, _ := parseMakeHandlerArgs([]string{"-r"}); !got.Resource {
		t.Errorf("short -r not honored")
	}
}

func TestParseMakeModelArgs(t *testing.T) {
	got, err := parseMakeModelArgs([]string{"--uuid", "--soft-deletes", "-m", "--dir=app/models"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := console.MakeModelOptions{UUID: true, SoftDeletes: true, Migration: true, Dir: "app/models"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseMakeMigrationArgs(t *testing.T) {
	got, err := parseMakeMigrationArgs([]string{"--create", "posts", "--uuid"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Create != "posts" || !got.UUID {
		t.Errorf("got %+v, want Create=posts UUID=true", got)
	}
	// --table in the equals form.
	if got, _ := parseMakeMigrationArgs([]string{"--table=users"}); got.Table != "users" {
		t.Errorf("--table=users -> Table = %q, want users", got.Table)
	}
}

func TestParseDirOnlyArgs(t *testing.T) {
	if got, err := parseDirOnlyArgs([]string{"--dir", "internal/x"}); err != nil || got != "internal/x" {
		t.Errorf("spaced --dir = (%q, %v), want internal/x", got, err)
	}
	if got, err := parseDirOnlyArgs([]string{"--dir=internal/x"}); err != nil || got != "internal/x" {
		t.Errorf("inline --dir = (%q, %v), want internal/x", got, err)
	}
	if got, err := parseDirOnlyArgs(nil); err != nil || got != "" {
		t.Errorf("no args = (%q, %v), want empty", got, err)
	}
	if _, err := parseDirOnlyArgs([]string{"--dir"}); err == nil {
		t.Errorf("dangling --dir: want error")
	}
	if _, err := parseDirOnlyArgs([]string{"Extra"}); err == nil {
		t.Errorf("stray positional: want error")
	}
}
