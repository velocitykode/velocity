package velocity

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/velocitykode/velocity/console"
)

// --- Database ---

type dbWipeCmd struct{}

func (dbWipeCmd) name() string        { return "db wipe" }
func (dbWipeCmd) description() string { return "Drop all tables" }
func (dbWipeCmd) run(a *App, args []string) error {
	// Only --force / -f is legal; reject any other token before the guard.
	if err := parseForceOnlyArgs(args); err != nil {
		return err
	}
	if err := guardProductionDataLoss(a, "db wipe", args); err != nil {
		return err
	}
	if err := a.Bootstrap(); err != nil {
		return err
	}
	return console.DBWipe(a.ormDB())
}

type dbSeedCmd struct{}

func (dbSeedCmd) name() string        { return "db seed" }
func (dbSeedCmd) description() string { return "Run the registered database seeders" }
func (dbSeedCmd) run(a *App, args []string) error {
	// Parse before the guard/Bootstrap so a typo fails fast. --only and
	// --force compose in any order; --force is consumed by the guard below.
	only, err := parseDBSeedArgs(args)
	if err != nil {
		return err
	}
	// Seeding writes rows, not drops them, but a development fixture set
	// landing in a production database is still an incident: same gate,
	// same --force escape hatch, its own wording.
	if err := guardProduction(a, "db seed", args, "this command writes seed data"); err != nil {
		return err
	}
	if err := a.Bootstrap(); err != nil {
		return err
	}
	ctx, stop := interruptContext()
	defer stop()
	return console.Seed(ctx, a.ormManager(), a.seeders.All(), console.SeedOptions{Only: only})
}

// interruptContext returns a context cancelled by SIGINT / SIGTERM, so a
// long seeding run stops between seeders on Ctrl-C instead of being killed
// mid-insert. Callers must call stop to release the signal handler.
func interruptContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// --- Cache ---

type cacheClearCmd struct{}

func (cacheClearCmd) name() string        { return "cache clear" }
func (cacheClearCmd) description() string { return "Flush the application cache" }
func (cacheClearCmd) run(a *App, args []string) error {
	if err := rejectNoArgs(args); err != nil {
		return err
	}
	if err := a.Bootstrap(); err != nil {
		return err
	}
	return console.CacheClear(a.Cache)
}

// --- Queue & scheduler ---

type queueWorkCmd struct{}

func (queueWorkCmd) name() string        { return "queue work" }
func (queueWorkCmd) description() string { return "Start processing queue jobs" }
func (queueWorkCmd) run(a *App, args []string) error {
	// Parse before Bootstrap so a typo fails fast without starting modules.
	opts, err := parseQueueWorkArgs(args)
	if err != nil {
		return err
	}
	if err := a.Bootstrap(); err != nil {
		return err
	}
	return console.QueueWork(a.Queue, queueWorkOptions(a, opts))
}

// queueWorkOptions attaches the app's services to the parsed `queue work`
// options: its logger, so worker errors flow through the configured log
// driver, and its event dispatcher, so the worker's job lifecycle events
// reach listeners and a permanently failed job reaches the error reporters
// through the dispatcher's failure-report bridge. With events disabled
// (WithoutEvents) the worker gets no dispatcher.
func queueWorkOptions(a *App, opts console.QueueWorkOptions) console.QueueWorkOptions {
	if a.Log != nil {
		opts.Logger = a.Log
	}
	if dispatch := buildEventDispatch(a); dispatch != nil {
		opts.Dispatcher = dispatch
	}
	return opts
}

type scheduleWorkCmd struct{}

func (scheduleWorkCmd) name() string        { return "schedule work" }
func (scheduleWorkCmd) description() string { return "Start the task scheduler" }
func (scheduleWorkCmd) run(a *App, args []string) error {
	if err := rejectNoArgs(args); err != nil {
		return err
	}
	if err := a.Bootstrap(); err != nil {
		return err
	}
	return console.ScheduleWork(a.Scheduler)
}

// --- Maintenance mode ---

type downCmd struct{}

func (downCmd) name() string        { return "down" }
func (downCmd) description() string { return "Put the application into maintenance mode" }
func (downCmd) run(a *App, args []string) error {
	opts, err := parseDownArgs(args)
	if err != nil {
		return err
	}
	return console.Down(opts)
}

type upCmd struct{}

func (upCmd) name() string        { return "up" }
func (upCmd) description() string { return "Bring the application out of maintenance mode" }
func (upCmd) run(a *App, args []string) error {
	if err := rejectNoArgs(args); err != nil {
		return err
	}
	return console.Up()
}

// --- Keys ---

type keyGenerateCmd struct{}

func (keyGenerateCmd) name() string        { return "key generate" }
func (keyGenerateCmd) description() string { return "Generate a new application key" }
func (keyGenerateCmd) run(a *App, args []string) error {
	if err := rejectNoArgs(args); err != nil {
		return err
	}
	return console.KeyGenerate()
}

// --- Server / build ---

type serveCmd struct{}

func (serveCmd) name() string        { return "serve" }
func (serveCmd) description() string { return "Start the development server" }
func (serveCmd) run(a *App, args []string) error {
	// ConfigFromEnv owns .env loading plus the APP_PORT/APP_ENV reads,
	// including the parse-failure warning and the deliberately fail-closed
	// empty APP_ENV. Pass cfg.Env through unchanged (even when empty) so the
	// single dev-server default lives in console.Serve, not here.
	cfg := ConfigFromEnv()
	opts, err := parseServeArgs(console.ServeOptions{
		Port:  cfg.Port,
		Env:   cfg.Env,
		Watch: true,
	}, args)
	if err != nil {
		return err
	}
	return console.Serve(opts)
}

type buildCmd struct{}

func (buildCmd) name() string        { return "build" }
func (buildCmd) description() string { return "Build for production" }
func (buildCmd) run(a *App, args []string) error {
	opts, err := parseBuildArgs(args)
	if err != nil {
		return err
	}
	return console.Build(opts)
}

// --- Custom command runner ---

type runCmd struct{}

func (runCmd) name() string        { return "run" }
func (runCmd) description() string { return "Run a custom command" }

// usageToken renders "run <command>" in help, signalling the required
// positional argument, while name() stays "run" for dispatch.
func (runCmd) usageToken() string { return "run <command>" }
func (runCmd) run(a *App, args []string) error {
	// Reject a flag-like first token before Bootstrap so `vel run --bogus`
	// fails fast like the other built-ins instead of starting modules and
	// reporting it as an unknown custom command.
	if len(args) > 0 && strings.HasPrefix(args[0], "-") {
		key, _, _ := strings.Cut(args[0], "=")
		return unknownToken(args[0], key)
	}
	if err := a.Bootstrap(); err != nil {
		return err
	}
	if len(args) == 0 {
		a.printUserCommands()
		return nil
	}
	cmdName := args[0]
	cmd, ok := a.commands.Get(cmdName)
	if !ok {
		a.printUserCommands()
		return fmt.Errorf("vel: unknown command %q", cmdName)
	}
	return cmd.Handle(a.Services, args[1:])
}

// --- Help ---

// helpCmd handles "help", "--help", and "-h". It's instantiated three times
// by the registry (one per alias) with the same description. Description is
// empty so help doesn't appear in the main command list - it's already the
// default output and printing "help" inside help is noise.
type helpCmd struct{ name_ string }

func (h helpCmd) name() string        { return h.name_ }
func (h helpCmd) description() string { return "" }
func (h helpCmd) run(a *App, args []string) error {
	if err := rejectNoArgs(args); err != nil {
		return err
	}
	a.printHelp()
	return nil
}

// --- Internal subprocess entry ---

// serveRunCmd is the internal entry point used by console.Serve when
// spawning the .vel/tmp/server subprocess. Not user-facing - don't document
// in printHelp. The child must go straight to a.serveHTTP() (which opens
// the HTTP listener and blocks); calling a.Serve() would re-enter the
// args-dispatch path (Serve → Run → runCommand("serve run") → this method)
// and recurse until the goroutine stack overflows.
//
// Its two-word name makes it a subcommand of the user-facing "serve": the
// dispatcher's longest-match rule sends `vel serve run` here and everything
// else (`vel serve --port 8080`) to serveCmd.
type serveRunCmd struct{}

func (serveRunCmd) name() string        { return "serve run" }
func (serveRunCmd) description() string { return "" }
func (serveRunCmd) run(a *App, args []string) error {
	if err := rejectNoArgs(args); err != nil {
		return err
	}
	return a.serveHTTP()
}
