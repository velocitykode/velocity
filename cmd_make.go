package velocity

import (
	"os"
	"strings"

	cli "github.com/velocitykode/velocity-cli"
	"github.com/velocitykode/velocity/console"
)

// requireMakeName prints the standard "X name is required" error block used
// by every make:* command that takes exactly a name argument. Exits the
// process so callers can safely index args[0] after returning.
func requireMakeName(args []string, label, usage string) {
	if len(args) > 0 {
		return
	}
	cli.Error(label + " name is required")
	cli.Muted("  Usage: vel " + usage + " [name]")
	os.Exit(1)
}

type makeHandlerCmd struct{}

func (makeHandlerCmd) name() string        { return "make:handler" }
func (makeHandlerCmd) description() string { return "Create a new handler" }
func (makeHandlerCmd) run(a *App, args []string) error {
	if len(args) == 0 {
		cli.Error("Handler name is required")
		cli.Newline()
		cli.Muted("Usage: vel make:handler [name]")
		cli.Newline()
		cli.Muted("Examples:")
		cli.Muted("  vel make:handler User")
		cli.Muted("  vel make:handler Admin/Dashboard --resource")
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
}

type makeModelCmd struct{}

func (makeModelCmd) name() string        { return "make:model" }
func (makeModelCmd) description() string { return "Create a new model" }
func (makeModelCmd) run(a *App, args []string) error {
	if len(args) == 0 {
		cli.Error("Model name is required")
		cli.Newline()
		cli.Muted("Usage: vel make:model [name]")
		cli.Newline()
		cli.Muted("Examples:")
		cli.Muted("  vel make:model User")
		cli.Muted("  vel make:model Post --uuid --soft-deletes")
		cli.Muted("  vel make:model Comment --migration")
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
}

type makeMigrationCmd struct{}

func (makeMigrationCmd) name() string        { return "make:migration" }
func (makeMigrationCmd) description() string { return "Create a new migration" }
func (makeMigrationCmd) run(a *App, args []string) error {
	if len(args) == 0 {
		cli.Error("Migration name is required")
		cli.Newline()
		cli.Muted("Usage: vel make:migration [name]")
		cli.Newline()
		cli.Muted("Examples:")
		cli.Muted("  vel make:migration create_posts")
		cli.Muted("  vel make:migration add_slug_to_posts --table=posts")
		cli.Muted("  vel make:migration create_comments --create=comments")
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
}

type makeMiddlewareCmd struct{}

func (makeMiddlewareCmd) name() string        { return "make:middleware" }
func (makeMiddlewareCmd) description() string { return "Create a new middleware" }
func (makeMiddlewareCmd) run(a *App, args []string) error {
	requireMakeName(args, "Middleware", "make:middleware")
	return console.MakeMiddleware(args[0], console.MakeMiddlewareOptions{})
}

type makeEventCmd struct{}

func (makeEventCmd) name() string        { return "make:event" }
func (makeEventCmd) description() string { return "Create a new event" }
func (makeEventCmd) run(a *App, args []string) error {
	requireMakeName(args, "Event", "make:event")
	return console.MakeEvent(args[0], console.MakeEventOptions{})
}

type makeListenerCmd struct{}

func (makeListenerCmd) name() string        { return "make:listener" }
func (makeListenerCmd) description() string { return "Create a new listener" }
func (makeListenerCmd) run(a *App, args []string) error {
	requireMakeName(args, "Listener", "make:listener")
	return console.MakeListener(args[0], console.MakeListenerOptions{})
}

type makeJobCmd struct{}

func (makeJobCmd) name() string        { return "make:job" }
func (makeJobCmd) description() string { return "Create a new job" }
func (makeJobCmd) run(a *App, args []string) error {
	requireMakeName(args, "Job", "make:job")
	return console.MakeJob(args[0], console.MakeJobOptions{})
}

type makeMailCmd struct{}

func (makeMailCmd) name() string        { return "make:mail" }
func (makeMailCmd) description() string { return "Create a new mailable" }
func (makeMailCmd) run(a *App, args []string) error {
	requireMakeName(args, "Mail", "make:mail")
	return console.MakeMail(args[0], console.MakeMailOptions{})
}

type makeNotificationCmd struct{}

func (makeNotificationCmd) name() string        { return "make:notification" }
func (makeNotificationCmd) description() string { return "Create a new notification" }
func (makeNotificationCmd) run(a *App, args []string) error {
	requireMakeName(args, "Notification", "make:notification")
	return console.MakeNotification(args[0], console.MakeNotificationOptions{})
}

type makeResourceCmd struct{}

func (makeResourceCmd) name() string        { return "make:resource" }
func (makeResourceCmd) description() string { return "Create a new API resource" }
func (makeResourceCmd) run(a *App, args []string) error {
	requireMakeName(args, "Resource", "make:resource")
	return console.MakeResource(args[0], console.MakeResourceOptions{})
}

type makePolicyCmd struct{}

func (makePolicyCmd) name() string        { return "make:policy" }
func (makePolicyCmd) description() string { return "Create a new policy" }
func (makePolicyCmd) run(a *App, args []string) error {
	requireMakeName(args, "Policy", "make:policy")
	return console.MakePolicy(args[0], console.MakePolicyOptions{})
}

type makeProviderCmd struct{}

func (makeProviderCmd) name() string        { return "make:provider" }
func (makeProviderCmd) description() string { return "Create a new service provider" }
func (makeProviderCmd) run(a *App, args []string) error {
	requireMakeName(args, "Provider", "make:provider")
	return console.MakeProvider(args[0], console.MakeProviderOptions{})
}

type makeCommandCmd struct{}

func (makeCommandCmd) name() string        { return "make:command" }
func (makeCommandCmd) description() string { return "Create a new command" }
func (makeCommandCmd) run(a *App, args []string) error {
	requireMakeName(args, "Command", "make:command")
	return console.MakeCommand(args[0], console.MakeCommandOptions{})
}
