package velocity

import (
	"fmt"
	"strings"

	"github.com/velocitykode/prism"
	"github.com/velocitykode/velocity/console"
)

// makeNameUsageHint prints the one-line usage shown by the name-only make:*
// commands. usage is the command token, e.g. "make:middleware".
func makeNameUsageHint(usage string) {
	prism.Muted("  Usage: vel " + usage + " [name]")
}

// requireMakeName returns an error when args is empty, after printing the
// unified usage block (a leading blank line, the one-line usage, and, when
// examples are supplied, an "Examples:" list). Callers that receive a nil
// error may safely index args[0]. Returning (instead of os.Exit) lets
// deferred cleanup in the CLI dispatcher and caller run before the process
// exits.
func requireMakeName(args []string, label, usage string, examples ...string) error {
	printUsage := func() {
		prism.Newline()
		prism.Muted("Usage: vel " + usage + " [name]")
		if len(examples) > 0 {
			prism.Newline()
			prism.Muted("Examples:")
			for _, ex := range examples {
				prism.Muted(ex)
			}
		}
	}
	if len(args) == 0 {
		printUsage()
		return fmt.Errorf("%s name is required", strings.ToLower(label))
	}
	// A flag-like first token is a typo, not the artifact name: reject it
	// instead of generating an artifact literally named "--bogus".
	if strings.HasPrefix(args[0], "-") {
		printUsage()
		return unknownToken(args[0], args[0])
	}
	return nil
}

type makeHandlerCmd struct{}

func (makeHandlerCmd) name() string        { return "make:handler" }
func (makeHandlerCmd) description() string { return "Create a new handler" }
func makeHandlerUsage() {
	prism.Newline()
	prism.Muted("Usage: vel make:handler [name]")
	prism.Newline()
	prism.Muted("Examples:")
	prism.Muted("  vel make:handler User")
	prism.Muted("  vel make:handler Admin/Dashboard --resource")
	prism.Muted("  vel make:handler User --dir internal/web/handlers")
}

func (makeHandlerCmd) run(a *App, args []string) error {
	if err := requireMakeName(args, "Handler", "make:handler",
		"  vel make:handler User",
		"  vel make:handler Admin/Dashboard --resource",
		"  vel make:handler User --dir internal/web/handlers"); err != nil {
		return err
	}
	opts, err := parseMakeHandlerArgs(args[1:])
	if err != nil {
		makeHandlerUsage()
		return err
	}
	return console.MakeHandler(args[0], opts)
}

type makeModelCmd struct{}

func (makeModelCmd) name() string        { return "make:model" }
func (makeModelCmd) description() string { return "Create a new model" }
func makeModelUsage() {
	prism.Newline()
	prism.Muted("Usage: vel make:model [name]")
	prism.Newline()
	prism.Muted("Examples:")
	prism.Muted("  vel make:model User")
	prism.Muted("  vel make:model Post --uuid --soft-deletes")
	prism.Muted("  vel make:model Comment --migration")
	prism.Muted("  vel make:model Invoice --dir internal/billing/models")
}

func (makeModelCmd) run(a *App, args []string) error {
	if err := requireMakeName(args, "Model", "make:model",
		"  vel make:model User",
		"  vel make:model Post --uuid --soft-deletes",
		"  vel make:model Comment --migration",
		"  vel make:model Invoice --dir internal/billing/models"); err != nil {
		return err
	}
	opts, err := parseMakeModelArgs(args[1:])
	if err != nil {
		makeModelUsage()
		return err
	}
	return console.MakeModel(args[0], opts)
}

type makeMigrationCmd struct{}

func (makeMigrationCmd) name() string        { return "make:migration" }
func (makeMigrationCmd) description() string { return "Create a new migration" }
func makeMigrationUsage() {
	prism.Newline()
	prism.Muted("Usage: vel make:migration [name]")
	prism.Newline()
	prism.Muted("Examples:")
	prism.Muted("  vel make:migration create_posts")
	prism.Muted("  vel make:migration add_slug_to_posts --table=posts")
	prism.Muted("  vel make:migration create_comments --create=comments")
	prism.Muted("  vel make:migration create_posts --dir db/migrations")
}

func (makeMigrationCmd) run(a *App, args []string) error {
	if err := requireMakeName(args, "Migration", "make:migration",
		"  vel make:migration create_posts",
		"  vel make:migration add_slug_to_posts --table=posts",
		"  vel make:migration create_comments --create=comments",
		"  vel make:migration create_posts --dir db/migrations"); err != nil {
		return err
	}
	opts, err := parseMakeMigrationArgs(args[1:])
	if err != nil {
		makeMigrationUsage()
		return err
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
	dir, err := parseDirOnlyArgs(args[1:])
	if err != nil {
		makeNameUsageHint("make:middleware")
		return err
	}
	return console.MakeMiddleware(args[0], console.MakeMiddlewareOptions{Dir: dir})
}

type makeEventCmd struct{}

func (makeEventCmd) name() string        { return "make:event" }
func (makeEventCmd) description() string { return "Create a new event" }
func (makeEventCmd) run(a *App, args []string) error {
	if err := requireMakeName(args, "Event", "make:event"); err != nil {
		return err
	}
	dir, err := parseDirOnlyArgs(args[1:])
	if err != nil {
		makeNameUsageHint("make:event")
		return err
	}
	return console.MakeEvent(args[0], console.MakeEventOptions{Dir: dir})
}

type makeListenerCmd struct{}

func (makeListenerCmd) name() string        { return "make:listener" }
func (makeListenerCmd) description() string { return "Create a new listener" }
func (makeListenerCmd) run(a *App, args []string) error {
	if err := requireMakeName(args, "Listener", "make:listener"); err != nil {
		return err
	}
	dir, err := parseDirOnlyArgs(args[1:])
	if err != nil {
		makeNameUsageHint("make:listener")
		return err
	}
	return console.MakeListener(args[0], console.MakeListenerOptions{Dir: dir})
}

type makeJobCmd struct{}

func (makeJobCmd) name() string        { return "make:job" }
func (makeJobCmd) description() string { return "Create a new job" }
func (makeJobCmd) run(a *App, args []string) error {
	if err := requireMakeName(args, "Job", "make:job"); err != nil {
		return err
	}
	dir, err := parseDirOnlyArgs(args[1:])
	if err != nil {
		makeNameUsageHint("make:job")
		return err
	}
	return console.MakeJob(args[0], console.MakeJobOptions{Dir: dir})
}

type makeMailCmd struct{}

func (makeMailCmd) name() string        { return "make:mail" }
func (makeMailCmd) description() string { return "Create a new mailable" }
func (makeMailCmd) run(a *App, args []string) error {
	if err := requireMakeName(args, "Mail", "make:mail"); err != nil {
		return err
	}
	dir, err := parseDirOnlyArgs(args[1:])
	if err != nil {
		makeNameUsageHint("make:mail")
		return err
	}
	return console.MakeMail(args[0], console.MakeMailOptions{Dir: dir})
}

type makeNotificationCmd struct{}

func (makeNotificationCmd) name() string        { return "make:notification" }
func (makeNotificationCmd) description() string { return "Create a new notification" }
func (makeNotificationCmd) run(a *App, args []string) error {
	if err := requireMakeName(args, "Notification", "make:notification"); err != nil {
		return err
	}
	dir, err := parseDirOnlyArgs(args[1:])
	if err != nil {
		makeNameUsageHint("make:notification")
		return err
	}
	return console.MakeNotification(args[0], console.MakeNotificationOptions{Dir: dir})
}

type makeResourceCmd struct{}

func (makeResourceCmd) name() string        { return "make:resource" }
func (makeResourceCmd) description() string { return "Create a new API resource" }
func (makeResourceCmd) run(a *App, args []string) error {
	if err := requireMakeName(args, "Resource", "make:resource"); err != nil {
		return err
	}
	dir, err := parseDirOnlyArgs(args[1:])
	if err != nil {
		makeNameUsageHint("make:resource")
		return err
	}
	return console.MakeResource(args[0], console.MakeResourceOptions{Dir: dir})
}

type makePolicyCmd struct{}

func (makePolicyCmd) name() string        { return "make:policy" }
func (makePolicyCmd) description() string { return "Create a new policy" }
func (makePolicyCmd) run(a *App, args []string) error {
	if err := requireMakeName(args, "Policy", "make:policy"); err != nil {
		return err
	}
	dir, err := parseDirOnlyArgs(args[1:])
	if err != nil {
		makeNameUsageHint("make:policy")
		return err
	}
	return console.MakePolicy(args[0], console.MakePolicyOptions{Dir: dir})
}

type makeProviderCmd struct{}

func (makeProviderCmd) name() string        { return "make:provider" }
func (makeProviderCmd) description() string { return "Create a new service provider" }
func (makeProviderCmd) run(a *App, args []string) error {
	if err := requireMakeName(args, "Provider", "make:provider"); err != nil {
		return err
	}
	dir, err := parseDirOnlyArgs(args[1:])
	if err != nil {
		makeNameUsageHint("make:provider")
		return err
	}
	return console.MakeProvider(args[0], console.MakeProviderOptions{Dir: dir})
}

type makeCommandCmd struct{}

func (makeCommandCmd) name() string        { return "make:command" }
func (makeCommandCmd) description() string { return "Create a new command" }
func (makeCommandCmd) run(a *App, args []string) error {
	if err := requireMakeName(args, "Command", "make:command"); err != nil {
		return err
	}
	dir, err := parseDirOnlyArgs(args[1:])
	if err != nil {
		makeNameUsageHint("make:command")
		return err
	}
	return console.MakeCommand(args[0], console.MakeCommandOptions{Dir: dir})
}
