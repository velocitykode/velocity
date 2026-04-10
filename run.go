package velocity

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/velocitykode/velocity/console"
)

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
	switch name {

	// --- Routes ---
	case "route:list":
		if err := a.Bootstrap(); err != nil {
			return err
		}
		return console.RouteList(a.Router)

	// --- Migrations ---
	case "migrate":
		if err := a.Bootstrap(); err != nil {
			return err
		}
		opts := console.MigrateOptions{}
		for _, arg := range args {
			if arg == "--pretend" {
				opts.Pretend = true
			}
		}
		return console.Migrate(a.DB, opts)

	case "migrate:fresh":
		if err := a.Bootstrap(); err != nil {
			return err
		}
		return console.MigrateFresh(a.DB)

	case "migrate:rollback":
		if err := a.Bootstrap(); err != nil {
			return err
		}
		steps := 1
		if len(args) >= 2 && (args[0] == "--step" || args[0] == "-s") {
			if n, err := strconv.Atoi(args[1]); err == nil {
				steps = n
			}
		}
		return console.MigrateRollback(a.DB, steps)

	// --- Code Generation ---
	case "make:handler":
		if len(args) == 0 {
			fmt.Println("Usage: vel make:handler [name]")
			fmt.Println()
			fmt.Println("Examples:")
			fmt.Println("  vel make:handler User")
			fmt.Println("  vel make:handler Admin/Dashboard --resource")
			os.Exit(1)
		}
		opts := console.MakeHandlerOptions{}
		for _, arg := range args[1:] {
			switch arg {
			case "--resource", "-r":
				opts.Resource = true
			case "--api":
				opts.API = true
			}
		}
		return console.MakeHandler(args[0], opts)

	case "make:model":
		if len(args) == 0 {
			fmt.Println("Usage: vel make:model [name]")
			fmt.Println()
			fmt.Println("Examples:")
			fmt.Println("  vel make:model User")
			fmt.Println("  vel make:model Post --uuid --soft-deletes")
			fmt.Println("  vel make:model Comment --migration")
			os.Exit(1)
		}
		opts := console.MakeModelOptions{}
		for _, arg := range args[1:] {
			switch arg {
			case "--uuid":
				opts.UUID = true
			case "--soft-deletes":
				opts.SoftDeletes = true
			case "--migration", "-m":
				opts.Migration = true
			}
		}
		return console.MakeModel(args[0], opts)

	case "make:migration":
		if len(args) == 0 {
			fmt.Println("Usage: vel make:migration [name]")
			fmt.Println()
			fmt.Println("Examples:")
			fmt.Println("  vel make:migration create_posts")
			fmt.Println("  vel make:migration add_slug_to_posts --table=posts")
			fmt.Println("  vel make:migration create_comments --create=comments")
			os.Exit(1)
		}
		opts := console.MakeMigrationOptions{}
		for i := 1; i < len(args); i++ {
			arg := args[i]
			switch {
			case arg == "--uuid":
				opts.UUID = true
			case arg == "--soft-deletes":
				opts.SoftDeletes = true
			case strings.HasPrefix(arg, "--create="):
				opts.Create = strings.TrimPrefix(arg, "--create=")
			case arg == "--create" && i+1 < len(args):
				i++
				opts.Create = args[i]
			case strings.HasPrefix(arg, "--table="):
				opts.Table = strings.TrimPrefix(arg, "--table=")
			case arg == "--table" && i+1 < len(args):
				i++
				opts.Table = args[i]
			}
		}
		return console.MakeMigration(args[0], opts)

	case "make:middleware":
		if len(args) == 0 {
			fmt.Println("Usage: vel make:middleware [name]")
			os.Exit(1)
		}
		return console.MakeMiddleware(args[0], console.MakeMiddlewareOptions{})

	case "make:event":
		if len(args) == 0 {
			fmt.Println("Usage: vel make:event [name]")
			os.Exit(1)
		}
		return console.MakeEvent(args[0], console.MakeEventOptions{})

	case "make:listener":
		if len(args) == 0 {
			fmt.Println("Usage: vel make:listener [name]")
			os.Exit(1)
		}
		return console.MakeListener(args[0], console.MakeListenerOptions{})

	case "make:job":
		if len(args) == 0 {
			fmt.Println("Usage: vel make:job [name]")
			os.Exit(1)
		}
		return console.MakeJob(args[0], console.MakeJobOptions{})

	case "make:mail":
		if len(args) == 0 {
			fmt.Println("Usage: vel make:mail [name]")
			os.Exit(1)
		}
		return console.MakeMail(args[0], console.MakeMailOptions{})

	case "make:notification":
		if len(args) == 0 {
			fmt.Println("Usage: vel make:notification [name]")
			os.Exit(1)
		}
		return console.MakeNotification(args[0], console.MakeNotificationOptions{})

	case "make:resource":
		if len(args) == 0 {
			fmt.Println("Usage: vel make:resource [name]")
			os.Exit(1)
		}
		return console.MakeResource(args[0], console.MakeResourceOptions{})

	case "make:policy":
		if len(args) == 0 {
			fmt.Println("Usage: vel make:policy [name]")
			os.Exit(1)
		}
		return console.MakePolicy(args[0], console.MakePolicyOptions{})

	case "make:provider":
		if len(args) == 0 {
			fmt.Println("Usage: vel make:provider [name]")
			os.Exit(1)
		}
		return console.MakeProvider(args[0], console.MakeProviderOptions{})

	case "make:command":
		if len(args) == 0 {
			fmt.Println("Usage: vel make:command [name]")
			os.Exit(1)
		}
		return console.MakeCommand(args[0], console.MakeCommandOptions{})

	// --- Database ---
	case "migrate:status":
		if err := a.Bootstrap(); err != nil {
			return err
		}
		return console.MigrateStatus(a.DB)

	case "db:wipe":
		if err := a.Bootstrap(); err != nil {
			return err
		}
		return console.DBWipe(a.DB)

	// --- Cache ---
	case "cache:clear":
		if err := a.Bootstrap(); err != nil {
			return err
		}
		return console.CacheClear(a.Cache)

	// --- Queue & Scheduler ---
	case "queue:work":
		if err := a.Bootstrap(); err != nil {
			return err
		}
		opts := console.QueueWorkOptions{}
		for i := 0; i < len(args); i++ {
			switch args[i] {
			case "--queue", "-q":
				if i+1 < len(args) {
					opts.Queue = args[i+1]
					i++
				}
			case "--tries":
				if i+1 < len(args) {
					if n, err := strconv.Atoi(args[i+1]); err == nil {
						opts.Tries = n
					}
					i++
				}
			case "--timeout":
				if i+1 < len(args) {
					if n, err := strconv.Atoi(args[i+1]); err == nil {
						opts.Timeout = n
					}
					i++
				}
			}
		}
		return console.QueueWork(a.Queue, opts)

	case "schedule:work":
		if err := a.Bootstrap(); err != nil {
			return err
		}
		return console.ScheduleWork(a.Scheduler)

	// --- Maintenance Mode ---
	case "down":
		opts := console.DownOptions{}
		for i := 0; i < len(args); i++ {
			switch {
			case strings.HasPrefix(args[i], "--secret="):
				opts.Secret = strings.TrimPrefix(args[i], "--secret=")
			case args[i] == "--secret" && i+1 < len(args):
				i++
				opts.Secret = args[i]
			case strings.HasPrefix(args[i], "--retry="):
				if n, err := strconv.Atoi(strings.TrimPrefix(args[i], "--retry=")); err == nil {
					opts.RetryAfter = n
				}
			case args[i] == "--retry" && i+1 < len(args):
				i++
				if n, err := strconv.Atoi(args[i]); err == nil {
					opts.RetryAfter = n
				}
			}
		}
		return console.Down(opts)

	case "up":
		return console.Up()

	// --- Keys ---
	case "key:generate":
		return console.KeyGenerate()

	// --- Server ---
	case "serve":
		opts := console.ServeOptions{
			Port:  "4000",
			Env:   "development",
			Watch: true,
		}
		for i := 0; i < len(args); i++ {
			switch args[i] {
			case "--port", "-p":
				if i+1 < len(args) {
					opts.Port = args[i+1]
					i++
				}
			case "--env", "-e":
				if i+1 < len(args) {
					opts.Env = args[i+1]
					i++
				}
			case "--no-watch":
				opts.Watch = false
			case "--tags":
				if i+1 < len(args) {
					opts.BuildTags = args[i+1]
					i++
				}
			}
		}
		return console.Serve(opts)

	// --- Build ---
	case "build":
		opts := console.BuildOptions{}
		for i := 0; i < len(args); i++ {
			switch args[i] {
			case "--output", "-o":
				if i+1 < len(args) {
					opts.Output = args[i+1]
					i++
				}
			case "--os":
				if i+1 < len(args) {
					opts.OS = args[i+1]
					i++
				}
			case "--arch":
				if i+1 < len(args) {
					opts.Arch = args[i+1]
					i++
				}
			case "--tags":
				if i+1 < len(args) {
					opts.Tags = args[i+1]
					i++
				}
			}
		}
		return console.Build(opts)

	case "help", "--help", "-h":
		a.printHelp()
		return nil

	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", name)
		a.printHelp()
		os.Exit(1)
		return nil
	}
}

func (a *App) printHelp() {
	fmt.Println()
	fmt.Println("  Velocity Framework")
	fmt.Println()
	fmt.Println("  Usage:")
	fmt.Println("    vel <command> [arguments]")
	fmt.Println()
	fmt.Println("  Server:")
	fmt.Println("    serve              Start the development server")
	fmt.Println("    build              Build for production")
	fmt.Println("    down               Put the application into maintenance mode")
	fmt.Println("    up                 Bring the application out of maintenance mode")
	fmt.Println()
	fmt.Println("  Database:")
	fmt.Println("    migrate            Run database migrations")
	fmt.Println("    migrate:fresh      Drop all tables and re-run migrations")
	fmt.Println("    migrate:rollback   Rollback the last migration batch")
	fmt.Println("    migrate:status     Show migration status")
	fmt.Println("    db:wipe            Drop all tables")
	fmt.Println()
	fmt.Println("  Queue & Scheduler:")
	fmt.Println("    queue:work         Start processing queue jobs")
	fmt.Println("    schedule:work      Start the task scheduler")
	fmt.Println()
	fmt.Println("  Cache:")
	fmt.Println("    cache:clear        Flush the application cache")
	fmt.Println()
	fmt.Println("  Code Generation:")
	fmt.Println("    make:handler       Create a new handler")
	fmt.Println("    make:model         Create a new model")
	fmt.Println("    make:migration     Create a new migration")
	fmt.Println("    make:middleware     Create a new middleware")
	fmt.Println("    make:event         Create a new event")
	fmt.Println("    make:listener      Create a new listener")
	fmt.Println("    make:job           Create a new job")
	fmt.Println("    make:mail          Create a new mailable")
	fmt.Println("    make:notification  Create a new notification")
	fmt.Println("    make:resource      Create a new API resource")
	fmt.Println("    make:policy        Create a new policy")
	fmt.Println("    make:provider      Create a new service provider")
	fmt.Println("    make:command       Create a new command")
	fmt.Println()
	fmt.Println("  Other:")
	fmt.Println("    route:list         List all registered routes")
	fmt.Println("    key:generate       Generate a new application key")
	fmt.Println()
}
