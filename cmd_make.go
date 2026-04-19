package velocity

import (
	"fmt"
	"strings"

	cli "github.com/velocitykode/velocity-cli"
	"github.com/velocitykode/velocity/console"
)

// requireMakeName returns an error when args is empty, after printing the
// standard usage hint. Callers that receive a nil error may safely index
// args[0]. Returning (instead of os.Exit) lets deferred cleanup in the
// CLI dispatcher and caller run before the process exits.
func requireMakeName(args []string, label, usage string) error {
	if len(args) > 0 {
		return nil
	}
	cli.Muted("  Usage: vel " + usage + " [name]")
	return fmt.Errorf("%s name is required", strings.ToLower(label))
}

type makeHandlerCmd struct{}

func (makeHandlerCmd) name() string        { return "make:handler" }
func (makeHandlerCmd) description() string { return "Create a new handler" }
func (makeHandlerCmd) run(a *App, args []string) error {
	if len(args) == 0 {
		cli.Newline()
		cli.Muted("Usage: vel make:handler [name]")
		cli.Newline()
		cli.Muted("Examples:")
		cli.Muted("  vel make:handler User")
		cli.Muted("  vel make:handler Admin/Dashboard --resource")
		return fmt.Errorf("handler name is required")
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
		cli.Newline()
		cli.Muted("Usage: vel make:model [name]")
		cli.Newline()
		cli.Muted("Examples:")
		cli.Muted("  vel make:model User")
		cli.Muted("  vel make:model Post --uuid --soft-deletes")
		cli.Muted("  vel make:model Comment --migration")
		return fmt.Errorf("model name is required")
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
		cli.Newline()
		cli.Muted("Usage: vel make:migration [name]")
		cli.Newline()
		cli.Muted("Examples:")
		cli.Muted("  vel make:migration create_posts")
		cli.Muted("  vel make:migration add_slug_to_posts --table=posts")
		cli.Muted("  vel make:migration create_comments --create=comments")
		return fmt.Errorf("migration name is required")
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
	if err := requireMakeName(args, "Middleware", "make:middleware"); err != nil {
		return err
	}
	return console.MakeMiddleware(args[0], console.MakeMiddlewareOptions{})
}

type makeEventCmd struct{}

func (makeEventCmd) name() string        { return "make:event" }
func (makeEventCmd) description() string { return "Create a new event" }
func (makeEventCmd) run(a *App, args []string) error {
	if err := requireMakeName(args, "Event", "make:event"); err != nil {
		return err
	}
	return console.MakeEvent(args[0], console.MakeEventOptions{})
}

type makeListenerCmd struct{}

func (makeListenerCmd) name() string        { return "make:listener" }
func (makeListenerCmd) description() string { return "Create a new listener" }
func (makeListenerCmd) run(a *App, args []string) error {
	if err := requireMakeName(args, "Listener", "make:listener"); err != nil {
		return err
	}
	return console.MakeListener(args[0], console.MakeListenerOptions{})
}

type makeJobCmd struct{}

func (makeJobCmd) name() string        { return "make:job" }
func (makeJobCmd) description() string { return "Create a new job" }
func (makeJobCmd) run(a *App, args []string) error {
	if err := requireMakeName(args, "Job", "make:job"); err != nil {
		return err
	}
	return console.MakeJob(args[0], console.MakeJobOptions{})
}

type makeMailCmd struct{}

func (makeMailCmd) name() string        { return "make:mail" }
func (makeMailCmd) description() string { return "Create a new mailable" }
func (makeMailCmd) run(a *App, args []string) error {
	if err := requireMakeName(args, "Mail", "make:mail"); err != nil {
		return err
	}
	return console.MakeMail(args[0], console.MakeMailOptions{})
}

type makeNotificationCmd struct{}

func (makeNotificationCmd) name() string        { return "make:notification" }
func (makeNotificationCmd) description() string { return "Create a new notification" }
func (makeNotificationCmd) run(a *App, args []string) error {
	if err := requireMakeName(args, "Notification", "make:notification"); err != nil {
		return err
	}
	return console.MakeNotification(args[0], console.MakeNotificationOptions{})
}

type makeResourceCmd struct{}

func (makeResourceCmd) name() string        { return "make:resource" }
func (makeResourceCmd) description() string { return "Create a new API resource" }
func (makeResourceCmd) run(a *App, args []string) error {
	if err := requireMakeName(args, "Resource", "make:resource"); err != nil {
		return err
	}
	return console.MakeResource(args[0], console.MakeResourceOptions{})
}

type makePolicyCmd struct{}

func (makePolicyCmd) name() string        { return "make:policy" }
func (makePolicyCmd) description() string { return "Create a new policy" }
func (makePolicyCmd) run(a *App, args []string) error {
	if err := requireMakeName(args, "Policy", "make:policy"); err != nil {
		return err
	}
	return console.MakePolicy(args[0], console.MakePolicyOptions{})
}

type makeProviderCmd struct{}

func (makeProviderCmd) name() string        { return "make:provider" }
func (makeProviderCmd) description() string { return "Create a new service provider" }
func (makeProviderCmd) run(a *App, args []string) error {
	if err := requireMakeName(args, "Provider", "make:provider"); err != nil {
		return err
	}
	return console.MakeProvider(args[0], console.MakeProviderOptions{})
}

type makeCommandCmd struct{}

func (makeCommandCmd) name() string        { return "make:command" }
func (makeCommandCmd) description() string { return "Create a new command" }
func (makeCommandCmd) run(a *App, args []string) error {
	if err := requireMakeName(args, "Command", "make:command"); err != nil {
		return err
	}
	return console.MakeCommand(args[0], console.MakeCommandOptions{})
}
