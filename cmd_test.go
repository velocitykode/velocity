package velocity

import (
	"os"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/chain"
)

// TestCommandRegistry_CoversEveryPreviousSwitchCase asserts the built-in
// command registry carries exactly the command tokens that the old
// run.go switch statement dispatched. If a future change drops or renames
// a built-in this test is the tripwire.
func TestCommandRegistry_CoversEveryPreviousSwitchCase(t *testing.T) {
	reg := newCommandRegistry()

	want := []string{
		// Routes
		"route:list",
		// Migrations
		"migrate", "migrate:fresh", "migrate:rollback", "migrate:status",
		// Code generation
		"make:handler", "make:model", "make:migration", "make:middleware",
		"make:event", "make:listener", "make:job", "make:mail",
		"make:notification", "make:resource", "make:policy", "make:provider",
		"make:command",
		// Database
		"db:wipe",
		// Cache
		"cache:clear",
		// Queue & scheduler
		"queue:work", "schedule:work",
		// Maintenance
		"down", "up",
		// Keys
		"key:generate",
		// Server / build
		"serve", "build",
		// Custom command runner
		"run",
		// Help aliases
		"help", "--help", "-h",
		// Internal subprocess entry
		"serve:run",
	}

	for _, name := range want {
		if _, ok := reg.get(name); !ok {
			t.Errorf("registry missing command %q", name)
		}
	}
}

// TestCommandRegistry_DescriptionsPresent asserts every non-alias command
// has a non-empty description. Empty strings are reserved for help aliases
// ("--help"/"-h"/"help") and the internal "serve:run" subprocess entry.
func TestCommandRegistry_DescriptionsPresent(t *testing.T) {
	reg := newCommandRegistry()

	allowEmpty := map[string]bool{
		"help":      true,
		"--help":    true,
		"-h":        true,
		"serve:run": true,
	}

	for _, c := range reg.order {
		if allowEmpty[c.name()] {
			continue
		}
		if strings.TrimSpace(c.description()) == "" {
			t.Errorf("command %q has empty description", c.name())
		}
	}
}

// TestCommandRegistry_HelpAliasesDispatchToPrintHelp smoke-tests the help
// path: dispatching "help", "--help", and "-h" must succeed without
// contacting any service (so a bare *App with no bootstrap works).
func TestCommandRegistry_HelpAliasesDispatchToPrintHelp(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	for _, alias := range []string{"help", "--help", "-h"} {
		cmd, ok := newCommandRegistry().get(alias)
		if !ok {
			t.Fatalf("alias %q not registered", alias)
		}
		if err := cmd.run(a, nil); err != nil {
			t.Errorf("help alias %q returned error: %v", alias, err)
		}
	}
}

// TestRunCmd_DispatchesToChainCommand exercises the custom-command runner:
// "vel run <name>" must bootstrap, look the user command up on a.commands,
// and invoke its Handle with the remaining args.
func TestRunCmd_DispatchesToChainCommand(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	var gotArgs []string
	var gotServices *app.Services
	a.Commands(func(r *chain.Commands) {
		r.Add(&stubCommand{
			name:        "seed",
			description: "Seed the database",
			handleFn: func(s *app.Services, args []string) error {
				gotServices = s
				gotArgs = args
				return nil
			},
		})
	})

	cmd, _ := newCommandRegistry().get("run")
	if err := cmd.run(a, []string{"seed", "one", "two"}); err != nil {
		t.Fatalf("run cmd returned error: %v", err)
	}

	if gotServices != a.Services {
		t.Error("chain command did not receive app's *Services")
	}
	if len(gotArgs) != 2 || gotArgs[0] != "one" || gotArgs[1] != "two" {
		t.Errorf("chain command got args %v, want [one two]", gotArgs)
	}
}

// TestRunCmd_NoArgsListsUserCommands asserts that "vel run" with no further
// args triggers the user-command listing without returning an error.
func TestRunCmd_NoArgsListsUserCommands(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	cmd, _ := newCommandRegistry().get("run")
	if err := cmd.run(a, nil); err != nil {
		t.Errorf("run cmd with no args returned error: %v", err)
	}
}

// TestApp_RunUnknownCommand_ReturnsError asserts that dispatching an unknown
// CLI token returns an error instead of calling os.Exit(1). The os.Exit
// path would bypass Serve()'s deferred shutdownCancel and any caller-
// installed defers; returning the error lets those run. If this test
// completes at all (instead of terminating the test binary) we know
// os.Exit was not invoked.
func TestApp_RunUnknownCommand_ReturnsError(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}

	saved := os.Args
	os.Args = []string{"vel", "nonexistent"}
	t.Cleanup(func() { os.Args = saved })

	err = a.Run()
	if err == nil {
		t.Fatal("Run() with unknown command returned nil error, want non-nil")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("Run() error = %q, want message containing %q", err.Error(), "nonexistent")
	}
}

// TestRunCommand_UnknownCustomCommand_ReturnsError covers the same
// regression for the "vel run <name>" dispatcher inside runCmd. The
// unknown-custom-command branch also previously called os.Exit(1); it
// must return an error instead so Serve()'s shutdownCancel can fire.
func TestRunCommand_UnknownCustomCommand_ReturnsError(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}

	cmd, _ := newCommandRegistry().get("run")
	err = cmd.run(a, []string{"no-such-chain-command"})
	if err == nil {
		t.Fatal("run cmd with unknown chain command returned nil error, want non-nil")
	}
	if !strings.Contains(err.Error(), "no-such-chain-command") {
		t.Errorf("run cmd error = %q, want message containing %q", err.Error(), "no-such-chain-command")
	}
}

// TestRequireMakeName_ReturnsErrorWhenEmpty asserts the shared helper used
// by make:* commands returns an error (rather than os.Exit) when the
// required name argument is missing. Exits would skip deferred cleanup.
func TestRequireMakeName_ReturnsErrorWhenEmpty(t *testing.T) {
	if err := requireMakeName(nil, "Middleware", "make:middleware"); err == nil {
		t.Fatal("requireMakeName(nil) returned nil error, want non-nil")
	}
	if err := requireMakeName([]string{"Foo"}, "Middleware", "make:middleware"); err != nil {
		t.Errorf("requireMakeName with arg returned error %v, want nil", err)
	}
}

// TestRunCmd_BootstrapsOnce asserts the "run" command path calls Bootstrap
// (needed so a.commands is populated from a.commandsFn). We verify by
// observing a provider-callback side effect.
func TestRunCmd_BootstrapsOnce(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	var booted bool
	a.Commands(func(r *chain.Commands) {
		booted = true
	})

	cmd, _ := newCommandRegistry().get("run")
	if err := cmd.run(a, nil); err != nil {
		t.Fatalf("run cmd returned error: %v", err)
	}
	if !booted {
		t.Error("run cmd did not trigger Bootstrap (commandsFn callback never fired)")
	}
}
