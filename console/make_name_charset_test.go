package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/console/scaffold"
)

// TestValidateMakeName_Charset locks the identifier charset contract for
// make:* name arguments: the name must match [A-Za-z][A-Za-z0-9_-]* with no
// "/" at all. Names flow unescaped through toPascalCase / toSnakeCase into
// text/template-generated Go source, so anything outside that set (quotes,
// backticks, semicolons, newlines, unicode confusables) must be rejected
// before it reaches a stub, and a "/" would survive into generated code as
// an invalid identifier.
func TestValidateMakeName_Charset(t *testing.T) {
	accepted := []string{
		"User",
		"SendEmail",
		"user_profile",
		"send-welcome-email",
		"APIToken",
		"create_users_table",
		"H2Database",
	}
	for _, name := range accepted {
		if err := scaffold.ValidateName(name); err != nil {
			t.Errorf("scaffold.ValidateName(%q) = %v, want nil", name, err)
		}
	}

	rejected := []struct {
		name    string
		offends string // substring expected in the error
	}{
		{`Foo"Bar`, `'"'`},
		{"Foo`Bar", "'`'"},
		{"Foo;Bar", "';'"},
		{"Foo\nBar", `'\n'`},
		{"Foo Bar", "' '"},
		{"Foo.Bar", "'.'"},
		{"Foo$Bar", "'$'"},
		{"Foo{Bar", "'{'"},
		{"Аdmin", "must start with a letter"}, // Cyrillic А (U+0410)
		{"Usеr", "not allowed"},               // Cyrillic е (U+0435)
		{"émail", "must start with a letter"}, // accented letter
		{"1Password", "must start with a letter"},
		{"_private", "must start with a letter"},
		{"-flag", "must start with a letter"},
		{"Admin/Users", "single path segment"}, // identifier names take no paths
		{"List/Foo", "single path segment"},
		{"Admin/2fa", "must start with a letter"}, // bad nested segment
		{`Admin/Us"ers`, `'"'`},                   // injection in nested segment
		{"Admin//Users", "empty path segment"},    // double slash
		{"Admin/", "empty path segment"},          // trailing slash
	}
	for _, tc := range rejected {
		err := scaffold.ValidateName(tc.name)
		if err == nil {
			t.Errorf("scaffold.ValidateName(%q) = nil, want charset rejection", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.offends) {
			t.Errorf("scaffold.ValidateName(%q) error %q does not name offender %q", tc.name, err, tc.offends)
		}
	}
}

// TestValidateMakeNestedName locks the contract for the slash-permitting
// variant used by callers that intentionally support nested output paths
// (make:handler names, make:grpc:service --dir): slashes separate segments
// and each segment must independently satisfy the identifier charset.
func TestValidateMakeNestedName(t *testing.T) {
	accepted := []string{
		"User",
		"Admin/Users",
		"admin/v2/Users",
		"internal/shared/grpc/services",
	}
	for _, name := range accepted {
		if err := scaffold.ValidateNestedName(name); err != nil {
			t.Errorf("scaffold.ValidateNestedName(%q) = %v, want nil", name, err)
		}
	}

	rejected := []struct {
		name    string
		offends string
	}{
		{"Admin/2fa", "must start with a letter"}, // bad nested segment
		{`Admin/Us"ers`, `'"'`},                   // injection in nested segment
		{"Admin//Users", "empty path segment"},    // double slash
		{"Admin/", "empty path segment"},          // trailing slash
		{"../tmp", "parent traversal"},
		{"Admin/../tmp", "parent traversal"},
		{".hidden/Foo", "starts with '.'"},
	}
	for _, tc := range rejected {
		err := scaffold.ValidateNestedName(tc.name)
		if err == nil {
			t.Errorf("scaffold.ValidateNestedName(%q) = nil, want rejection", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.offends) {
			t.Errorf("scaffold.ValidateNestedName(%q) error %q does not name offender %q", tc.name, err, tc.offends)
		}
	}
}

// TestMake_RejectsSourceInjection asserts every Make* generator rejects a
// name whose characters would survive into generated Go source. It mirrors
// TestMake_RejectsTraversal: one row per command so a future generator that
// skips scaffold.ValidateName fails here.
func TestMake_RejectsSourceInjection(t *testing.T) {
	// A double quote plus newline: enough to break out of any string literal
	// or identifier position in a stub template.
	const evil = "Owned\"\npackage evil"

	cases := []struct {
		name  string
		run   func(name string) error
		setup func(t *testing.T)
	}{
		{
			name: "make:command",
			run:  func(n string) error { return MakeCommand(n, MakeCommandOptions{}) },
		},
		{
			name: "make:event",
			run:  func(n string) error { return MakeEvent(n, MakeEventOptions{}) },
		},
		{
			name: "make:handler",
			run:  func(n string) error { return MakeHandler(n, MakeHandlerOptions{}) },
		},
		{
			name: "make:job",
			run:  func(n string) error { return MakeJob(n, MakeJobOptions{}) },
		},
		{
			name: "make:listener",
			run:  func(n string) error { return MakeListener(n, MakeListenerOptions{}) },
		},
		{
			name: "make:mail",
			run:  func(n string) error { return MakeMail(n, MakeMailOptions{}) },
		},
		{
			name: "make:middleware",
			run:  func(n string) error { return MakeMiddleware(n, MakeMiddlewareOptions{}) },
		},
		{
			name: "make:migration",
			run:  func(n string) error { return MakeMigration(n, MakeMigrationOptions{}) },
		},
		{
			name: "make:model",
			run:  func(n string) error { return MakeModel(n, MakeModelOptions{}) },
		},
		{
			name: "make:notification",
			run:  func(n string) error { return MakeNotification(n, MakeNotificationOptions{}) },
		},
		{
			name: "make:policy",
			run:  func(n string) error { return MakePolicy(n, MakePolicyOptions{}) },
		},
		{
			name: "make:provider",
			run:  func(n string) error { return MakeProvider(n, MakeProviderOptions{}) },
		},
		{
			name: "make:resource",
			run:  func(n string) error { return MakeResource(n, MakeResourceOptions{}) },
		},
		{
			name: "make:grpc:service",
			run:  func(n string) error { return MakeGRPCService(n, MakeGRPCServiceOptions{}) },
			setup: func(t *testing.T) {
				writeFakeGoMod(t, "acme/app")
			},
		},
		{
			name: "make:grpc:rpc service arg",
			run:  func(n string) error { return MakeGRPCRPC(n, "Hello", MakeGRPCRPCOptions{}) },
		},
		{
			name: "make:grpc:rpc rpc arg",
			run:  func(n string) error { return MakeGRPCRPC("Greeter", n, MakeGRPCRPCOptions{}) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Chdir(tmp)
			if tc.setup != nil {
				tc.setup(t)
			}

			err := tc.run(evil)
			if err == nil {
				t.Fatalf("%s accepted injection input %q", tc.name, evil)
			}
			if !strings.Contains(strings.ToLower(err.Error()), "invalid") {
				t.Errorf("%s: expected charset-rejection error, got %v", tc.name, err)
			}
			// Nothing should have been scaffolded from the bad name.
			entries, _ := os.ReadDir(tmp)
			for _, e := range entries {
				if e.Name() == "go.mod" {
					continue
				}
				t.Errorf("%s: unexpected output %s from rejected name", tc.name, e.Name())
			}
		})
	}
}

// TestMake_RejectsSlashInIdentifierNames asserts every generator whose name
// becomes a Go/proto identifier (everything except make:handler, which maps
// slashes to nested directories) rejects a slash-separated input. A name like
// "List/Foo" previously passed the shared validator and reached templating as
// an invalid identifier (e.g. an rpc stub `rpc List/Foo(...)`).
func TestMake_RejectsSlashInIdentifierNames(t *testing.T) {
	const evil = "List/Foo"

	cases := []struct {
		name  string
		run   func(name string) error
		setup func(t *testing.T)
	}{
		{
			name: "make:command",
			run:  func(n string) error { return MakeCommand(n, MakeCommandOptions{}) },
		},
		{
			name: "make:event",
			run:  func(n string) error { return MakeEvent(n, MakeEventOptions{}) },
		},
		{
			name: "make:job",
			run:  func(n string) error { return MakeJob(n, MakeJobOptions{}) },
		},
		{
			name: "make:listener",
			run:  func(n string) error { return MakeListener(n, MakeListenerOptions{}) },
		},
		{
			name: "make:mail",
			run:  func(n string) error { return MakeMail(n, MakeMailOptions{}) },
		},
		{
			name: "make:middleware",
			run:  func(n string) error { return MakeMiddleware(n, MakeMiddlewareOptions{}) },
		},
		{
			name: "make:migration",
			run:  func(n string) error { return MakeMigration(n, MakeMigrationOptions{}) },
		},
		{
			name: "make:model",
			run:  func(n string) error { return MakeModel(n, MakeModelOptions{}) },
		},
		{
			name: "make:notification",
			run:  func(n string) error { return MakeNotification(n, MakeNotificationOptions{}) },
		},
		{
			name: "make:policy",
			run:  func(n string) error { return MakePolicy(n, MakePolicyOptions{}) },
		},
		{
			name: "make:provider",
			run:  func(n string) error { return MakeProvider(n, MakeProviderOptions{}) },
		},
		{
			name: "make:resource",
			run:  func(n string) error { return MakeResource(n, MakeResourceOptions{}) },
		},
		{
			name: "make:grpc:service",
			run:  func(n string) error { return MakeGRPCService(n, MakeGRPCServiceOptions{}) },
			setup: func(t *testing.T) {
				writeFakeGoMod(t, "acme/app")
			},
		},
		{
			name: "make:grpc:rpc service arg",
			run:  func(n string) error { return MakeGRPCRPC(n, "Hello", MakeGRPCRPCOptions{}) },
		},
		{
			name: "make:grpc:rpc rpc arg",
			run:  func(n string) error { return MakeGRPCRPC("Greeter", n, MakeGRPCRPCOptions{}) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Chdir(tmp)
			if tc.setup != nil {
				tc.setup(t)
			}

			err := tc.run(evil)
			if err == nil {
				t.Fatalf("%s accepted slash-separated input %q", tc.name, evil)
			}
			if !strings.Contains(err.Error(), "single path segment") {
				t.Errorf("%s: expected single-segment rejection, got %v", tc.name, err)
			}
			// Nothing should have been scaffolded from the bad name.
			entries, _ := os.ReadDir(tmp)
			for _, e := range entries {
				if e.Name() == "go.mod" {
					continue
				}
				t.Errorf("%s: unexpected output %s from rejected name", tc.name, e.Name())
			}
		})
	}
}

// TestMakeMigration_TableFlagValidation covers the --create/--table flag
// values, which land verbatim in the generated migration's TableName. Valid
// SQL identifiers ([A-Za-z_][A-Za-z0-9_]*) pass; anything that could break
// out of the generated string literal is rejected with the offending
// character named.
func TestMakeMigration_TableFlagValidation(t *testing.T) {
	accepted := []string{"users", "user_accounts", "_private", "Sessions", "t2"}
	for _, table := range accepted {
		for _, flag := range []string{"--create", "--table"} {
			if err := validateTableName(flag, table); err != nil {
				t.Errorf("validateTableName(%q, %q) = %v, want nil", flag, table, err)
			}
		}
	}
	// Empty means "flag not passed" and must not error.
	if err := validateTableName("--create", ""); err != nil {
		t.Errorf("validateTableName with empty value = %v, want nil", err)
	}

	rejected := []struct {
		table   string
		offends string
	}{
		{`users"`, `'"'`},
		{"users`", "'`'"},
		{"users;drop", "';'"},
		{"users\ntable", `'\n'`},
		{"user table", "' '"},
		{"users-archive", "'-'"},
		{"2fa_codes", "must start with a letter or underscore"},
		{"užers", "not allowed"},
	}
	for _, tc := range rejected {
		err := validateTableName("--create", tc.table)
		if err == nil {
			t.Errorf("validateTableName(--create, %q) = nil, want rejection", tc.table)
			continue
		}
		if !strings.Contains(err.Error(), tc.offends) {
			t.Errorf("validateTableName(--create, %q) error %q does not name offender %q", tc.table, err, tc.offends)
		}
	}

	// End to end: a clean name with a hostile --create flag must not write a
	// migration file.
	t.Chdir(t.TempDir())
	err := MakeMigration("CreateUsersTable", MakeMigrationOptions{Create: `users" //`})
	if err == nil {
		t.Fatal("MakeMigration accepted hostile --create value")
	}
	if !strings.Contains(err.Error(), "--create") {
		t.Errorf("error %q does not mention the offending flag", err)
	}
	if _, statErr := os.Stat(filepath.Join("database", "migrations")); !os.IsNotExist(statErr) {
		t.Error("migration output appeared despite rejected --create value")
	}

	// And the alter-table path both ways.
	if err := MakeMigration("AddIndexToUsers", MakeMigrationOptions{Table: "users"}); err != nil {
		t.Fatalf("MakeMigration rejected valid --table value: %v", err)
	}
	if err := MakeMigration("AddIndexToOrders", MakeMigrationOptions{Table: "orders;--"}); err == nil {
		t.Fatal("MakeMigration accepted hostile --table value")
	}
}
