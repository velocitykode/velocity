package velocity

import (
	"fmt"
	"os"

	cli "github.com/velocitykode/velocity-cli"
)

// command is the internal interface for built-in CLI commands. Each root
// case in the previous run.go switch is now a small type implementing this
// interface. The registry below holds one instance of each built-in and the
// dispatcher (App.runCommand) looks them up by name, falling through to
// user-registered chain commands on miss.
//
// User-facing custom commands implement the separate chain.Command interface
// and are invoked via `vel run <name>`.
type command interface {
	// name returns the command token users type (e.g. "migrate:fresh").
	name() string

	// description is the single-line help text shown by printHelp. May be
	// empty for commands that are intentionally omitted from help output
	// (e.g. the internal "serve:run" subprocess entry point).
	description() string

	// run executes the command with the already-split argument list (i.e.
	// os.Args[2:] for top-level commands). It must not re-read os.Args.
	run(a *App, args []string) error
}

// commandRegistry holds the built-in command set. It's constructed once per
// App.Run call (cheap — each command is a zero-sized struct) and looked up
// by name. Built-ins win over chain commands, but chain commands remain
// reachable through the "run" command.
type commandRegistry struct {
	byName map[string]command
	order  []command
}

func newCommandRegistry() *commandRegistry {
	r := &commandRegistry{byName: make(map[string]command)}
	r.add(
		// Routes
		routeListCmd{},
		// Migrations
		migrateCmd{},
		migrateFreshCmd{},
		migrateRollbackCmd{},
		migrateStatusCmd{},
		// Code generation
		makeHandlerCmd{},
		makeModelCmd{},
		makeMigrationCmd{},
		makeMiddlewareCmd{},
		makeEventCmd{},
		makeListenerCmd{},
		makeJobCmd{},
		makeMailCmd{},
		makeNotificationCmd{},
		makeResourceCmd{},
		makePolicyCmd{},
		makeProviderCmd{},
		makeCommandCmd{},
		makeGRPCServiceCmd{},
		makeGRPCRPCCmd{},
		makeGRPCGenCmd{},
		// Database
		dbWipeCmd{},
		// Cache
		cacheClearCmd{},
		// Queue & scheduler
		queueWorkCmd{},
		scheduleWorkCmd{},
		// Maintenance mode
		downCmd{},
		upCmd{},
		// Keys
		keyGenerateCmd{},
		// Server / build
		serveCmd{},
		buildCmd{},
		// Custom command runner
		runCmd{},
		// Help
		helpCmd{name_: "help"},
		helpCmd{name_: "--help"},
		helpCmd{name_: "-h"},
		// Internal subprocess entry (not in help output)
		serveRunCmd{},
	)
	return r
}

func (r *commandRegistry) add(cmds ...command) {
	for _, c := range cmds {
		r.byName[c.name()] = c
		r.order = append(r.order, c)
	}
}

func (r *commandRegistry) get(name string) (command, bool) {
	c, ok := r.byName[name]
	return c, ok
}

// Run dispatches CLI commands or starts the HTTP server.
// If os.Args contains a command (e.g. "route:list"), it runs that command.
// With no arguments, it displays available commands.
func (a *App) Run() error {
	if len(os.Args) > 1 {
		return a.runCommand(os.Args[1], os.Args[2:])
	}
	a.printHelp()
	return nil
}

func (a *App) runCommand(name string, args []string) error {
	reg := newCommandRegistry()
	if cmd, ok := reg.get(name); ok {
		return cmd.run(a, args)
	}
	// Return the error instead of os.Exit(1) so deferred cleanup (notably
	// Serve()'s shutdownCancel and any caller-installed defers) gets a chance
	// to run. The top-level caller (main.go via Serve() → Run()) is
	// responsible for converting the returned error into a non-zero exit code.
	a.printHelp()
	return fmt.Errorf("vel: unknown command %q", name)
}

func (a *App) printHelp() {
	cli.Newline()
	cli.Bold("  Velocity Framework")
	cli.Newline()
	cli.Muted("Usage:")
	cli.Muted("  vel <command> [arguments]")
	cli.Newline()

	cli.Info("Server")
	cli.Muted("  serve              Start the development server")
	cli.Muted("  build              Build for production")
	cli.Muted("  down               Put the application into maintenance mode")
	cli.Muted("  up                 Bring the application out of maintenance mode")
	cli.Newline()

	cli.Info("Database")
	cli.Muted("  migrate            Run database migrations")
	cli.Muted("  migrate:fresh      Drop all tables and re-run migrations")
	cli.Muted("  migrate:rollback   Rollback the last migration batch")
	cli.Muted("  migrate:status     Show migration status")
	cli.Muted("  db:wipe            Drop all tables")
	cli.Newline()

	cli.Info("Queue & Scheduler")
	cli.Muted("  queue:work         Start processing queue jobs")
	cli.Muted("  schedule:work      Start the task scheduler")
	cli.Newline()

	cli.Info("Cache")
	cli.Muted("  cache:clear        Flush the application cache")
	cli.Newline()

	cli.Info("Code Generation")
	cli.Muted("  make:handler       Create a new handler")
	cli.Muted("  make:model         Create a new model")
	cli.Muted("  make:migration     Create a new migration")
	cli.Muted("  make:middleware     Create a new middleware")
	cli.Muted("  make:event         Create a new event")
	cli.Muted("  make:listener      Create a new listener")
	cli.Muted("  make:job           Create a new job")
	cli.Muted("  make:mail          Create a new mailable")
	cli.Muted("  make:notification  Create a new notification")
	cli.Muted("  make:resource      Create a new API resource")
	cli.Muted("  make:policy        Create a new policy")
	cli.Muted("  make:provider      Create a new service provider")
	cli.Muted("  make:command       Create a new command")
	cli.Muted("  make:grpc:service  Scaffold a gRPC service (proto + impl + provider)")
	cli.Muted("  make:grpc:rpc      Add an rpc to an existing gRPC service")
	cli.Muted("  make:grpc:gen      Run `buf generate` in api/proto")
	cli.Newline()

	cli.Info("Custom Commands")
	cli.Muted("  run <command>      Run a custom command")
	cli.Newline()

	cli.Info("Other")
	cli.Muted("  route:list         List all registered routes")
	cli.Muted("  key:generate       Generate a new application key")
	cli.Newline()
}

// printUserCommands lists all registered user commands with their descriptions.
func (a *App) printUserCommands() {
	if a.commands == nil || len(a.commands.All()) == 0 {
		cli.Newline()
		cli.Muted("No custom commands registered.")
		cli.Newline()
		cli.Muted("Create one with: vel make:command <Name>")
		cli.Newline()
		return
	}

	cli.Newline()
	cli.Bold("  Custom Commands")
	cli.Newline()

	for _, cmd := range a.commands.All() {
		cli.Muted(fmt.Sprintf("  %-20s%s", cmd.Name(), cmd.Description()))
	}
	cli.Newline()
}
