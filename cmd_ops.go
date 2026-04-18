package velocity

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	cli "github.com/velocitykode/velocity-cli"
	"github.com/velocitykode/velocity/console"
)

// --- Database ---

type dbWipeCmd struct{}

func (dbWipeCmd) name() string        { return "db:wipe" }
func (dbWipeCmd) description() string { return "Drop all tables" }
func (dbWipeCmd) run(a *App, args []string) error {
	if err := a.Bootstrap(); err != nil {
		return err
	}
	return console.DBWipe(a.DB)
}

// --- Cache ---

type cacheClearCmd struct{}

func (cacheClearCmd) name() string        { return "cache:clear" }
func (cacheClearCmd) description() string { return "Flush the application cache" }
func (cacheClearCmd) run(a *App, args []string) error {
	if err := a.Bootstrap(); err != nil {
		return err
	}
	return console.CacheClear(a.Cache)
}

// --- Queue & scheduler ---

type queueWorkCmd struct{}

func (queueWorkCmd) name() string        { return "queue:work" }
func (queueWorkCmd) description() string { return "Start processing queue jobs" }
func (queueWorkCmd) run(a *App, args []string) error {
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
}

type scheduleWorkCmd struct{}

func (scheduleWorkCmd) name() string        { return "schedule:work" }
func (scheduleWorkCmd) description() string { return "Start the task scheduler" }
func (scheduleWorkCmd) run(a *App, args []string) error {
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
}

type upCmd struct{}

func (upCmd) name() string        { return "up" }
func (upCmd) description() string { return "Bring the application out of maintenance mode" }
func (upCmd) run(a *App, args []string) error {
	return console.Up()
}

// --- Keys ---

type keyGenerateCmd struct{}

func (keyGenerateCmd) name() string        { return "key:generate" }
func (keyGenerateCmd) description() string { return "Generate a new application key" }
func (keyGenerateCmd) run(a *App, args []string) error {
	return console.KeyGenerate()
}

// --- Server / build ---

type serveCmd struct{}

func (serveCmd) name() string        { return "serve" }
func (serveCmd) description() string { return "Start the development server" }
func (serveCmd) run(a *App, args []string) error {
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
}

type buildCmd struct{}

func (buildCmd) name() string        { return "build" }
func (buildCmd) description() string { return "Build for production" }
func (buildCmd) run(a *App, args []string) error {
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
}

// --- Custom command runner ---

type runCmd struct{}

func (runCmd) name() string        { return "run" }
func (runCmd) description() string { return "Run a custom command" }
func (runCmd) run(a *App, args []string) error {
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
		cli.Error(fmt.Sprintf("Unknown command: %s", cmdName))
		cli.Newline()
		a.printUserCommands()
		os.Exit(1)
	}
	return cmd.Handle(a.Services, args[1:])
}

// --- Help ---

// helpCmd handles "help", "--help", and "-h". It's instantiated three times
// by the registry (one per alias) with the same description. Description is
// empty so help doesn't appear in the main command list — it's already the
// default output and printing "help" inside help is noise.
type helpCmd struct{ name_ string }

func (h helpCmd) name() string        { return h.name_ }
func (h helpCmd) description() string { return "" }
func (h helpCmd) run(a *App, args []string) error {
	a.printHelp()
	return nil
}

// --- Internal subprocess entry ---

// serveRunCmd is the internal entry point used by console.Serve when
// spawning the .vel/tmp/server subprocess. Not user-facing — don't document
// in printHelp. The child must go straight to a.Serve() (which opens the
// HTTP listener and blocks); without this case the child falls through to
// printHelp and exits, leaving nothing on the port.
type serveRunCmd struct{}

func (serveRunCmd) name() string        { return "serve:run" }
func (serveRunCmd) description() string { return "" }
func (serveRunCmd) run(a *App, args []string) error {
	return a.Serve()
}
