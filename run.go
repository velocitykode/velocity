package velocity

import (
	"fmt"
	"os"
	"strconv"

	"github.com/velocitykode/velocity/console"
)

// Run dispatches CLI commands or starts the HTTP server.
// If os.Args contains a command (e.g. "route:list"), it runs that command.
// Otherwise it falls through to Serve().
func (a *App) Run() error {
	if len(os.Args) > 1 {
		return a.runCommand(os.Args[1], os.Args[2:])
	}
	return a.Serve()
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
		return console.Migrate(a.DB)

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

	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\nAvailable commands:\n", name)
		fmt.Fprintln(os.Stderr, "  serve            Start the development server")
		fmt.Fprintln(os.Stderr, "  build            Build for production")
		fmt.Fprintln(os.Stderr, "  migrate          Run database migrations")
		fmt.Fprintln(os.Stderr, "  migrate:fresh    Drop all tables and re-run migrations")
		fmt.Fprintln(os.Stderr, "  migrate:rollback Rollback the last migration batch")
		fmt.Fprintln(os.Stderr, "  route:list       List all registered routes")
		fmt.Fprintln(os.Stderr, "  make:handler     Create a new handler")
		fmt.Fprintln(os.Stderr, "  key:generate     Generate a new application key")
		os.Exit(1)
		return nil
	}
}
