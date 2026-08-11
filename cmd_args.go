package velocity

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/velocitykode/velocity/console"
)

// This file holds the standalone argument parsers for the built-in CLI
// commands. They are deliberately pure (no *App, no services, no I/O) so the
// dispatcher can parse a command's flags BEFORE bootstrapping the application
// - a typo then fails fast without spinning up modules - and so each parser
// can be unit-tested in isolation the way parseMakeGRPCServiceArgs is.
//
// Every parser rejects tokens it does not recognise rather than silently
// dropping them: an unknown flag yields "unknown flag: <flag>", a stray
// positional yields "unexpected argument: <arg>", and a flag missing its
// value yields "flag <flag> needs a value". This is a deliberate,
// user-visible behaviour change - previously-ignored typos now error.

// flagValue resolves the value for a value-taking flag at args[i]. key/val are
// the strings.Cut halves of args[i] and hasEq reports whether an '=' was
// present. For the "--flag=value" form it returns val without advancing; for
// the spaced "--flag value" form it consumes args[i+1] and returns the
// advanced index so the caller can assign it back to its loop counter. A
// missing or empty value is an error.
func flagValue(args []string, i int, key, val string, hasEq bool) (string, int, error) {
	if hasEq {
		if val == "" {
			return "", i, fmt.Errorf("flag %s needs a value", key)
		}
		return val, i, nil
	}
	return spacedValue(args, i, key)
}

// spacedValue resolves the spaced "--flag value" value at args[i+1] for the
// flag named key, returning the value and the advanced index. A missing or
// empty token errors with "flag <key> needs a value"; a flag-like token
// (anything beginning with "-") is rejected as an unknown flag rather than
// silently swallowed as this flag's value (e.g. `--queue --bogus`).
func spacedValue(args []string, i int, key string) (string, int, error) {
	if i+1 >= len(args) || args[i+1] == "" {
		return "", i, fmt.Errorf("flag %s needs a value", key)
	}
	next := args[i+1]
	if strings.HasPrefix(next, "-") {
		nkey, _, _ := strings.Cut(next, "=")
		return "", i, unknownToken(next, nkey)
	}
	return next, i + 1, nil
}

// dirValue resolves the value of a --dir / --dir=VALUE flag at args[i],
// returning the value and the (possibly advanced) index. An empty or missing
// value errors rather than silently falling back to the command's default
// output directory.
func dirValue(args []string, i int, arg string) (string, int, error) {
	if v, ok := strings.CutPrefix(arg, "--dir="); ok {
		if v == "" {
			return "", i, fmt.Errorf("flag --dir needs a value")
		}
		return v, i, nil
	}
	// arg == "--dir"
	return spacedValue(args, i, "--dir")
}

// unknownToken classifies an unrecognised token: anything beginning with "-"
// is reported as an unknown flag (naming key, the part before any '='),
// everything else as an unexpected positional argument.
func unknownToken(arg, key string) error {
	if strings.HasPrefix(arg, "-") {
		return fmt.Errorf("unknown flag: %s", key)
	}
	return fmt.Errorf("unexpected argument: %s", arg)
}

// rejectNoArgs is the parser for built-in commands that accept no user
// arguments. It errors on the first token so a typo like `migrate:status
// extra` or `cache:clear --bogus` fails fast instead of being silently
// ignored.
func rejectNoArgs(args []string) error {
	if len(args) > 0 {
		return unknownToken(args[0], args[0])
	}
	return nil
}

// parseForceOnlyArgs is the parser for the destructive commands (db:wipe,
// migrate:fresh) that accept only the --force / -f flag consumed by
// guardProductionDataLoss. Every other token is rejected.
func parseForceOnlyArgs(args []string) error {
	for _, arg := range args {
		switch arg {
		case "--force", "-f":
		default:
			return unknownToken(arg, arg)
		}
	}
	return nil
}

// parseMigrateArgs parses `migrate` arguments. Only --pretend is legal.
func parseMigrateArgs(args []string) (console.MigrateOptions, error) {
	var opts console.MigrateOptions
	for _, arg := range args {
		switch arg {
		case "--pretend":
			opts.Pretend = true
		default:
			return opts, unknownToken(arg, arg)
		}
	}
	return opts, nil
}

// parseRollbackArgs parses `migrate:rollback` arguments and returns the number
// of batches to roll back (default 1). It accepts --step N, -s N and
// --step=N in any position, and tolerates the --force / -f flag that the
// production-data-loss guard consumes separately. A missing step value, a
// non-integer, or a value below 1 is an error.
func parseRollbackArgs(args []string) (int, error) {
	steps := 1
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--force" || arg == "-f":
			// Consumed by guardProductionDataLoss; legal here, ignored.
		case arg == "--step" || arg == "-s":
			if i+1 >= len(args) {
				return 0, fmt.Errorf("flag %s needs a value", arg)
			}
			i++
			n, err := atoiStep(arg, args[i])
			if err != nil {
				return 0, err
			}
			steps = n
		case strings.HasPrefix(arg, "--step="):
			n, err := atoiStep("--step", strings.TrimPrefix(arg, "--step="))
			if err != nil {
				return 0, err
			}
			steps = n
		default:
			return 0, unknownToken(arg, arg)
		}
	}
	return steps, nil
}

// atoiStep parses a --step value, requiring a positive integer.
func atoiStep(flag, raw string) (int, error) {
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("flag %s needs an integer value, got %q", flag, raw)
	}
	if n < 1 {
		return 0, fmt.Errorf("flag %s must be >= 1, got %d", flag, n)
	}
	return n, nil
}

// parseQueueWorkArgs parses `queue:work` arguments. The Logger field is
// attached by the caller after bootstrapping, not here.
func parseQueueWorkArgs(args []string) (console.QueueWorkOptions, error) {
	var opts console.QueueWorkOptions
	for i := 0; i < len(args); i++ {
		arg := args[i]
		key, val, hasEq := strings.Cut(arg, "=")
		switch key {
		case "--queue", "-q":
			v, ni, err := flagValue(args, i, key, val, hasEq)
			if err != nil {
				return opts, err
			}
			opts.Queue, i = v, ni
		case "--tries":
			v, ni, err := flagValue(args, i, key, val, hasEq)
			if err != nil {
				return opts, err
			}
			n, err := strconv.Atoi(v)
			if err != nil {
				return opts, fmt.Errorf("flag %s needs an integer value, got %q", key, v)
			}
			opts.Tries, i = n, ni
		case "--timeout":
			v, ni, err := flagValue(args, i, key, val, hasEq)
			if err != nil {
				return opts, err
			}
			n, err := strconv.Atoi(v)
			if err != nil {
				return opts, fmt.Errorf("flag %s needs an integer value, got %q", key, v)
			}
			opts.Timeout, i = n, ni
		default:
			return opts, unknownToken(arg, key)
		}
	}
	return opts, nil
}

// parseDownArgs parses `down` arguments. A non-integer --retry value errors.
func parseDownArgs(args []string) (console.DownOptions, error) {
	var opts console.DownOptions
	for i := 0; i < len(args); i++ {
		arg := args[i]
		key, val, hasEq := strings.Cut(arg, "=")
		switch key {
		case "--secret":
			v, ni, err := flagValue(args, i, key, val, hasEq)
			if err != nil {
				return opts, err
			}
			opts.Secret, i = v, ni
		case "--retry":
			v, ni, err := flagValue(args, i, key, val, hasEq)
			if err != nil {
				return opts, err
			}
			n, err := strconv.Atoi(v)
			if err != nil {
				return opts, fmt.Errorf("flag %s needs an integer value, got %q", key, v)
			}
			opts.RetryAfter, i = n, ni
		default:
			return opts, unknownToken(arg, key)
		}
	}
	return opts, nil
}

// parseServeArgs applies `serve` flag overrides onto base. base carries the
// env-bootstrapped defaults (port, env, watch) computed by the caller; this
// parser only handles the flag loop.
func parseServeArgs(base console.ServeOptions, args []string) (console.ServeOptions, error) {
	opts := base
	for i := 0; i < len(args); i++ {
		arg := args[i]
		key, val, hasEq := strings.Cut(arg, "=")
		switch key {
		case "--port", "-p":
			v, ni, err := flagValue(args, i, key, val, hasEq)
			if err != nil {
				return opts, err
			}
			opts.Port, i = v, ni
		case "--env", "-e":
			v, ni, err := flagValue(args, i, key, val, hasEq)
			if err != nil {
				return opts, err
			}
			opts.Env, i = v, ni
		case "--tags":
			v, ni, err := flagValue(args, i, key, val, hasEq)
			if err != nil {
				return opts, err
			}
			opts.BuildTags, i = v, ni
		case "--no-watch":
			opts.Watch = false
		default:
			return opts, unknownToken(arg, key)
		}
	}
	return opts, nil
}

// parseBuildArgs parses `build` arguments.
func parseBuildArgs(args []string) (console.BuildOptions, error) {
	var opts console.BuildOptions
	for i := 0; i < len(args); i++ {
		arg := args[i]
		key, val, hasEq := strings.Cut(arg, "=")
		switch key {
		case "--output", "-o":
			v, ni, err := flagValue(args, i, key, val, hasEq)
			if err != nil {
				return opts, err
			}
			opts.Output, i = v, ni
		case "--os":
			v, ni, err := flagValue(args, i, key, val, hasEq)
			if err != nil {
				return opts, err
			}
			opts.OS, i = v, ni
		case "--arch":
			v, ni, err := flagValue(args, i, key, val, hasEq)
			if err != nil {
				return opts, err
			}
			opts.Arch, i = v, ni
		case "--tags":
			v, ni, err := flagValue(args, i, key, val, hasEq)
			if err != nil {
				return opts, err
			}
			opts.Tags, i = v, ni
		default:
			return opts, unknownToken(arg, key)
		}
	}
	return opts, nil
}

// parseMakeHandlerArgs parses the post-name arguments for `make:handler`.
func parseMakeHandlerArgs(args []string) (console.MakeHandlerOptions, error) {
	var opts console.MakeHandlerOptions
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--resource" || arg == "-r":
			opts.Resource = true
		case arg == "--api":
			opts.API = true
		case arg == "--dir" || strings.HasPrefix(arg, "--dir="):
			v, ni, err := dirValue(args, i, arg)
			if err != nil {
				return opts, err
			}
			opts.Dir, i = v, ni
		default:
			return opts, unknownToken(arg, arg)
		}
	}
	return opts, nil
}

// parseMakeModelArgs parses the post-name arguments for `make:model`.
func parseMakeModelArgs(args []string) (console.MakeModelOptions, error) {
	var opts console.MakeModelOptions
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--uuid":
			opts.UUID = true
		case arg == "--soft-deletes":
			opts.SoftDeletes = true
		case arg == "--migration" || arg == "-m":
			opts.Migration = true
		case arg == "--dir" || strings.HasPrefix(arg, "--dir="):
			v, ni, err := dirValue(args, i, arg)
			if err != nil {
				return opts, err
			}
			opts.Dir, i = v, ni
		default:
			return opts, unknownToken(arg, arg)
		}
	}
	return opts, nil
}

// parseMakeMigrationArgs parses the post-name arguments for `make:migration`.
// --create and --table take a value in either form; a dangling flag with no
// value errors rather than being dropped.
func parseMakeMigrationArgs(args []string) (console.MakeMigrationOptions, error) {
	var opts console.MakeMigrationOptions
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--uuid":
			opts.UUID = true
		case arg == "--soft-deletes":
			opts.SoftDeletes = true
		case strings.HasPrefix(arg, "--create="):
			v := strings.TrimPrefix(arg, "--create=")
			if v == "" {
				return opts, fmt.Errorf("flag --create needs a value")
			}
			opts.Create = v
		case arg == "--create":
			v, ni, err := spacedValue(args, i, "--create")
			if err != nil {
				return opts, err
			}
			opts.Create, i = v, ni
		case strings.HasPrefix(arg, "--table="):
			v := strings.TrimPrefix(arg, "--table=")
			if v == "" {
				return opts, fmt.Errorf("flag --table needs a value")
			}
			opts.Table = v
		case arg == "--table":
			v, ni, err := spacedValue(args, i, "--table")
			if err != nil {
				return opts, err
			}
			opts.Table, i = v, ni
		case arg == "--dir" || strings.HasPrefix(arg, "--dir="):
			v, ni, err := dirValue(args, i, arg)
			if err != nil {
				return opts, err
			}
			opts.Dir, i = v, ni
		default:
			return opts, unknownToken(arg, arg)
		}
	}
	return opts, nil
}

// parseDirOnlyArgs parses the post-name arguments for the make:* commands that
// accept only an optional --dir override (middleware, event, listener, job,
// mail, notification, resource, policy, provider, command). Any other flag is
// an unknown flag and any extra positional is an unexpected argument.
func parseDirOnlyArgs(args []string) (string, error) {
	dir := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--dir" || strings.HasPrefix(arg, "--dir="):
			v, ni, err := dirValue(args, i, arg)
			if err != nil {
				return "", err
			}
			dir, i = v, ni
		default:
			return "", unknownToken(arg, arg)
		}
	}
	return dir, nil
}
