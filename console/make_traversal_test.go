package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMake_RejectsTraversal asserts every public Make* generator rejects a
// classic path-traversal input. The gen commands all derive the output
// file path from the user-supplied name, so a single missed validator lets a
// caller scaffold files outside the project root. This table exists to lock
// the contract in place: any new Make* generator added to the package should
// gain a row here.
func TestMake_RejectsTraversal(t *testing.T) {
	const evil = "../../tmp/owned"

	type runner func(name string) error

	cases := []struct {
		name   string
		run    runner
		setup  func(t *testing.T) // optional pre-setup (e.g. go.mod)
		expect string             // optional substring on top of "invalid"
	}{
		{
			name: "gen command",
			run:  func(n string) error { return MakeCommand(n, MakeCommandOptions{}) },
		},
		{
			name: "gen event",
			run:  func(n string) error { return MakeEvent(n, MakeEventOptions{}) },
		},
		{
			name: "gen handler",
			run:  func(n string) error { return MakeHandler(n, MakeHandlerOptions{}) },
		},
		{
			name: "gen job",
			run:  func(n string) error { return MakeJob(n, MakeJobOptions{}) },
		},
		{
			name: "gen listener",
			run:  func(n string) error { return MakeListener(n, MakeListenerOptions{}) },
		},
		{
			name: "gen mail",
			run:  func(n string) error { return MakeMail(n, MakeMailOptions{}) },
		},
		{
			name: "gen middleware",
			run:  func(n string) error { return MakeMiddleware(n, MakeMiddlewareOptions{}) },
		},
		{
			name: "gen migration",
			run:  func(n string) error { return MakeMigration(n, MakeMigrationOptions{}) },
		},
		{
			name: "gen model",
			run:  func(n string) error { return MakeModel(n, MakeModelOptions{}) },
		},
		{
			name: "gen notification",
			run:  func(n string) error { return MakeNotification(n, MakeNotificationOptions{}) },
		},
		{
			name: "gen policy",
			run:  func(n string) error { return MakePolicy(n, MakePolicyOptions{}) },
		},
		{
			name: "gen module",
			run:  func(n string) error { return MakeModule(n, MakeModuleOptions{}) },
		},
		{
			name: "gen resource",
			run:  func(n string) error { return MakeResource(n, MakeResourceOptions{}) },
		},
		{
			name: "gen grpc service",
			run:  func(n string) error { return MakeGRPCService(n, MakeGRPCServiceOptions{}) },
			setup: func(t *testing.T) {
				writeFakeGoMod(t, "acme/app")
			},
		},
		{
			name: "gen grpc rpc",
			run:  func(n string) error { return MakeGRPCRPC(n, "Hello", MakeGRPCRPCOptions{}) },
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
				t.Fatalf("%s accepted traversal input %q", tc.name, evil)
			}
			lower := strings.ToLower(err.Error())
			if !strings.Contains(lower, "invalid") && !strings.Contains(lower, "parent traversal") {
				t.Errorf("%s: expected traversal-rejection error, got %v", tc.name, err)
			}
			// Nothing should have been written outside the project root.
			if _, statErr := os.Stat(filepath.Join(tmp, "..", "owned")); statErr == nil {
				t.Errorf("%s: a file appeared outside the project root", tc.name)
			}
		})
	}
}

// TestMake_AcceptsValidName confirms each Make* generator still accepts a
// reasonable PascalCase name and writes the expected file. It is the
// counterpart to TestMake_RejectsTraversal so the validators do not over-fit
// and start rejecting legitimate input.
func TestMake_AcceptsValidName(t *testing.T) {
	cases := []struct {
		name     string
		run      func(string) error
		setup    func(t *testing.T)
		input    string
		expected string
	}{
		{
			name:     "gen command",
			run:      func(n string) error { return MakeCommand(n, MakeCommandOptions{}) },
			input:    "SendEmail",
			expected: filepath.Join("internal", "commands", "send_email.go"),
		},
		{
			name:     "gen event",
			run:      func(n string) error { return MakeEvent(n, MakeEventOptions{}) },
			input:    "UserRegistered",
			expected: filepath.Join("internal", "events", "user_registered.go"),
		},
		{
			name:     "gen handler",
			run:      func(n string) error { return MakeHandler(n, MakeHandlerOptions{}) },
			input:    "Dashboard",
			expected: filepath.Join("internal", "handlers", "dashboard.go"),
		},
		{
			name:     "gen job",
			run:      func(n string) error { return MakeJob(n, MakeJobOptions{}) },
			input:    "ProcessImport",
			expected: filepath.Join("internal", "jobs", "process_import.go"),
		},
		{
			name:     "gen listener",
			run:      func(n string) error { return MakeListener(n, MakeListenerOptions{}) },
			input:    "SendWelcomeEmail",
			expected: filepath.Join("internal", "listeners", "send_welcome_email.go"),
		},
		{
			name:     "gen mail",
			run:      func(n string) error { return MakeMail(n, MakeMailOptions{}) },
			input:    "WelcomeEmail",
			expected: filepath.Join("internal", "mail", "welcome_email.go"),
		},
		{
			name:     "gen middleware",
			run:      func(n string) error { return MakeMiddleware(n, MakeMiddlewareOptions{}) },
			input:    "Auth",
			expected: filepath.Join("internal", "middleware", "auth.go"),
		},
		{
			name:     "gen model",
			run:      func(n string) error { return MakeModel(n, MakeModelOptions{}) },
			input:    "User",
			expected: filepath.Join("internal", "models", "user.go"),
		},
		{
			name:     "gen notification",
			run:      func(n string) error { return MakeNotification(n, MakeNotificationOptions{}) },
			input:    "OrderShipped",
			expected: filepath.Join("internal", "notifications", "order_shipped.go"),
		},
		{
			name:     "gen policy",
			run:      func(n string) error { return MakePolicy(n, MakePolicyOptions{}) },
			input:    "Post",
			expected: filepath.Join("internal", "policies", "post.go"),
		},
		{
			name:     "gen module",
			run:      func(n string) error { return MakeModule(n, MakeModuleOptions{}) },
			input:    "Mailer",
			expected: filepath.Join("internal", "providers", "mailer.go"),
		},
		{
			name:     "gen resource",
			run:      func(n string) error { return MakeResource(n, MakeResourceOptions{}) },
			input:    "User",
			expected: filepath.Join("internal", "resources", "user.go"),
		},
		{
			name: "gen grpc service",
			run:  func(n string) error { return MakeGRPCService(n, MakeGRPCServiceOptions{}) },
			setup: func(t *testing.T) {
				writeFakeGoMod(t, "acme/app")
			},
			input:    "Foo",
			expected: filepath.Join("api", "proto", "foo", "v1", "foo.proto"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if tc.setup != nil {
				tc.setup(t)
			}

			if err := tc.run(tc.input); err != nil {
				t.Fatalf("%s rejected valid input %q: %v", tc.name, tc.input, err)
			}
			if _, err := os.Stat(tc.expected); err != nil {
				t.Errorf("%s: expected %s to exist: %v", tc.name, tc.expected, err)
			}
		})
	}
}

// TestMakeMigration_AcceptsValidName is split out from TestMake_AcceptsValidName
// because gen migration produces a timestamp-prefixed filename that cannot
// be matched as an exact string.
func TestMakeMigration_AcceptsValidName(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := MakeMigration("CreateUsersTable", MakeMigrationOptions{Create: "users"}); err != nil {
		t.Fatalf("MakeMigration rejected valid input: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join("database", "migrations"))
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	var found bool
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "_create_users_table.go") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a *_create_users_table.go file, got %v", entries)
	}
}
