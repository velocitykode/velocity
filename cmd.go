package velocity

import (
	"fmt"
	"os"

	"github.com/velocitykode/prism"
	"github.com/velocitykode/velocity/contract"
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

// usageTokener is an optional interface a command may implement to render a
// help token different from its name() - e.g. "run <command>" instead of
// "run". Commands that don't implement it fall back to name(). Same
// optional-interface pattern as the command interface above.
type usageTokener interface {
	usageToken() string
}

// usageToken returns the token printHelp displays for c: its usageToken() when
// the command implements usageTokener, otherwise its name().
func usageToken(c command) string {
	if t, ok := c.(usageTokener); ok {
		return t.usageToken()
	}
	return c.name()
}

// commandSection is one titled group of commands in the help output. The
// section list is the single source of truth for both dispatch (every
// command is registered) and printHelp (titled sections render in order). A
// section with an empty title is hidden from help - used for the help aliases
// and the internal serve:run entry point, which are dispatchable but not
// documented.
type commandSection struct {
	title string
	cmds  []command
}

// commandRegistry holds the built-in command set. It's constructed once per
// App.Run call (cheap - each command is a zero-sized struct) and looked up
// by name. Built-ins win over chain commands, but chain commands remain
// reachable through the "run" command.
type commandRegistry struct {
	byName   map[string]command
	order    []command
	sections []commandSection
}

func newCommandRegistry() *commandRegistry {
	r := &commandRegistry{byName: make(map[string]command)}
	r.addSection("Server",
		serveCmd{},
		buildCmd{},
		downCmd{},
		upCmd{},
	)
	r.addSection("Database",
		migrateCmd{},
		migrateFreshCmd{},
		migrateRollbackCmd{},
		migrateStatusCmd{},
		dbWipeCmd{},
	)
	r.addSection("Queue & Scheduler",
		queueWorkCmd{},
		scheduleWorkCmd{},
	)
	r.addSection("Cache",
		cacheClearCmd{},
	)
	r.addSection("Code Generation",
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
	)
	r.addSection("Custom Commands",
		runCmd{},
	)
	r.addSection("Other",
		routeListCmd{},
		keyGenerateCmd{},
	)
	// Hidden group (empty title): help aliases and the internal subprocess
	// entry. Registered for dispatch and order coverage, omitted from help.
	r.addSection("",
		helpCmd{name_: "help"},
		helpCmd{name_: "--help"},
		helpCmd{name_: "-h"},
		serveRunCmd{},
	)
	return r
}

func (r *commandRegistry) addSection(title string, cmds ...command) {
	r.sections = append(r.sections, commandSection{title: title, cmds: cmds})
	r.add(cmds...)
	// r.order is the flattened section sequence - the sections are the single
	// source of truth for dispatch, help, and registry iteration alike, so
	// removing a command from a section drops it from all three with no other
	// edit.
	r.order = append(r.order, cmds...)
}

func (r *commandRegistry) add(cmds ...command) {
	for _, c := range cmds {
		r.byName[c.name()] = c
	}
}

// helpPadWidth returns the column width printHelp left-pads command tokens to:
// the longest rendered token among help-visible commands (those in a titled
// section with a non-empty description), plus two spaces of gutter.
func (r *commandRegistry) helpPadWidth() int {
	max := 0
	for _, sec := range r.sections {
		if sec.title == "" {
			continue
		}
		for _, c := range sec.cmds {
			if c.description() == "" {
				continue
			}
			if n := len(usageToken(c)); n > max {
				max = n
			}
		}
	}
	return max + 2
}

func (r *commandRegistry) get(name string) (command, bool) {
	c, ok := r.byName[name]
	return c, ok
}

// hasForceFlag reports whether args carries the --force / -f flag used to
// override guardProductionDataLoss.
func hasForceFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--force" || arg == "-f" {
			return true
		}
	}
	return false
}

// guardProductionDataLoss refuses to run a command that destroys database
// data (db:wipe, migrate:fresh, migrate:rollback) in a production-class
// environment unless the operator passed --force. Same fail-secure stance
// as the other production gates (session store validation, gRPC reflection,
// mail log-driver warning): contract.IsProductionEnv treats "production",
// "prod", "staging", and any unrecognised APP_ENV value as production, so a
// typo'd APP_ENV cannot disable the gate.
//
// The guard lives in the cmd layer by design and runs BEFORE Bootstrap so a
// refused command never executes the provider lifecycle. Programmatic
// callers of console.DBWipe / console.MigrateFresh / console.MigrateRollback
// are unaffected; those functions are unguarded by contract.
func guardProductionDataLoss(a *App, name string, args []string) error {
	if hasForceFlag(args) {
		return nil
	}
	env := ""
	if a != nil && a.config != nil {
		env = a.config.Env
	}
	if !contract.IsProductionEnv(env) {
		return nil
	}
	return fmt.Errorf("vel: refusing to run %q in a production environment (APP_ENV=%q): this command destroys database data; pass --force to proceed", name, env)
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
	prism.Newline()
	prism.Bold("  Velocity Framework")
	prism.Newline()
	prism.Muted("Usage:")
	prism.Muted("  vel <command> [arguments]")
	prism.Newline()

	reg := newCommandRegistry()
	width := reg.helpPadWidth()
	for _, sec := range reg.sections {
		if sec.title == "" {
			continue
		}
		prism.Info(sec.title)
		for _, c := range sec.cmds {
			if c.description() == "" {
				continue
			}
			prism.Muted(fmt.Sprintf("  %-*s%s", width, usageToken(c), c.description()))
		}
		prism.Newline()
	}
}

// printUserCommands lists all registered user commands with their descriptions.
func (a *App) printUserCommands() {
	if a.commands == nil || len(a.commands.All()) == 0 {
		prism.Newline()
		prism.Muted("No custom commands registered.")
		prism.Newline()
		prism.Muted("Create one with: vel make:command <Name>")
		prism.Newline()
		return
	}

	prism.Newline()
	prism.Bold("  Custom Commands")
	prism.Newline()

	for _, cmd := range a.commands.All() {
		prism.Muted(fmt.Sprintf("  %-20s%s", cmd.Name(), cmd.Description()))
	}
	prism.Newline()
}
