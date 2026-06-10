package velocity

import (
	"strings"
	"testing"
)

// destructiveDBCommands lists every built-in command that drops or truncates
// database data and must therefore refuse to run in a production-class
// environment without --force. If a new drop/truncate command is added to the
// registry, add it here so it inherits the guard coverage.
var destructiveDBCommands = []string{"db:wipe", "migrate:fresh", "migrate:rollback"}

// TestDestructiveDBCommands_RefuseInProductionWithoutForce is the V2-06
// regression test: db:wipe / migrate:fresh / migrate:rollback must refuse in
// every production-class APP_ENV (including "staging" and unknown values,
// which contract.IsProductionEnv treats as production fail-secure) unless
// --force was passed. The refusal must fire BEFORE Bootstrap so no provider
// lifecycle runs.
func TestDestructiveDBCommands_RefuseInProductionWithoutForce(t *testing.T) {
	for _, name := range destructiveDBCommands {
		for _, env := range []string{"production", "prod", "staging", "some-typo"} {
			t.Run(name+"/"+env, func(t *testing.T) {
				a, err := NewTestApp()
				if err != nil {
					t.Fatalf("NewTestApp: %v", err)
				}
				a.config.Env = env

				cmd, ok := newCommandRegistry().get(name)
				if !ok {
					t.Fatalf("command %q not registered", name)
				}

				err = cmd.run(a, nil)
				if err == nil {
					t.Fatalf("%s with APP_ENV=%q returned nil error, want production refusal", name, env)
				}
				if !strings.Contains(err.Error(), "--force") {
					t.Errorf("%s refusal error = %q, want mention of --force", name, err.Error())
				}
				if a.bootstrapped {
					t.Errorf("%s refusal ran Bootstrap; guard must fire before the provider lifecycle", name)
				}
			})
		}
	}
}

// TestDestructiveDBCommands_ForceOverridesProductionGuard pins the escape
// hatch: --force (and the -f alias) lets the command proceed in production.
// The test app has no database configured, so the underlying console
// functions warn and return nil; a nil error proves the guard stepped aside.
func TestDestructiveDBCommands_ForceOverridesProductionGuard(t *testing.T) {
	for _, name := range destructiveDBCommands {
		for _, flag := range []string{"--force", "-f"} {
			t.Run(name+"/"+flag, func(t *testing.T) {
				a, err := NewTestApp()
				if err != nil {
					t.Fatalf("NewTestApp: %v", err)
				}
				a.config.Env = "production"
				// Production-class envs require APP_KEY. Wire a valid
				// 32-byte key so the post---force Bootstrap exercises
				// only the data-loss guard, not the APP_KEY production
				// gate.
				a.config.Key = strings.Repeat("k", 32)
				a.config.Crypto.Key = a.config.Key

				cmd, _ := newCommandRegistry().get(name)
				if err := cmd.run(a, []string{flag}); err != nil {
					t.Fatalf("%s %s in production returned %v, want nil", name, flag, err)
				}
				if !a.bootstrapped {
					t.Errorf("%s %s did not Bootstrap", name, flag)
				}
			})
		}
	}
}

// TestDestructiveDBCommands_UnguardedInDevAndTestEnvs covers the
// non-production envs: the guard must not change behavior there, with or
// without --force.
func TestDestructiveDBCommands_UnguardedInDevAndTestEnvs(t *testing.T) {
	for _, name := range destructiveDBCommands {
		for _, env := range []string{"development", "dev", "test", "testing", "local", ""} {
			t.Run(name+"/env="+env, func(t *testing.T) {
				a, err := NewTestApp()
				if err != nil {
					t.Fatalf("NewTestApp: %v", err)
				}
				a.config.Env = env

				cmd, _ := newCommandRegistry().get(name)
				if err := cmd.run(a, nil); err != nil {
					t.Fatalf("%s with APP_ENV=%q returned %v, want nil", name, env, err)
				}
			})
		}
	}
}

// TestHasForceFlag pins the flag scan: --force / -f anywhere in args, no
// false positives on similar tokens.
func TestHasForceFlag(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{}, false},
		{[]string{"--force"}, true},
		{[]string{"-f"}, true},
		{[]string{"--step", "2", "--force"}, true},
		{[]string{"--forceful"}, false},
		{[]string{"force"}, false},
	}
	for _, c := range cases {
		if got := hasForceFlag(c.args); got != c.want {
			t.Errorf("hasForceFlag(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}
