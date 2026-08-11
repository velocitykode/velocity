package velocity

import (
	"fmt"
	"strings"

	"github.com/velocitykode/prism"
	"github.com/velocitykode/velocity/console"
)

// genNameUsageHint prints the one-line usage shown by the name-only gen
// commands. usage is the command token, e.g. "gen middleware".
func genNameUsageHint(usage string) {
	prism.Muted("  Usage: vel " + usage + " [name]")
}

// requireGenName returns an error when args is empty, after printing the
// unified usage block (a leading blank line, the one-line usage, and, when
// examples are supplied, an "Examples:" list). Callers that receive a nil
// error may safely index args[0]. Returning (instead of os.Exit) lets
// deferred cleanup in the CLI dispatcher and caller run before the process
// exits.
func requireGenName(args []string, label, usage string, examples ...string) error {
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

type genHandlerCmd struct{}

func (genHandlerCmd) name() string        { return "gen handler" }
func (genHandlerCmd) description() string { return "Create a new handler" }
func genHandlerUsage() {
	prism.Newline()
	prism.Muted("Usage: vel gen handler [name]")
	prism.Newline()
	prism.Muted("Examples:")
	prism.Muted("  vel gen handler User")
	prism.Muted("  vel gen handler Admin/Dashboard --resource")
	prism.Muted("  vel gen handler User --dir internal/web/handlers")
}

func (genHandlerCmd) run(a *App, args []string) error {
	if err := requireGenName(args, "Handler", "gen handler",
		"  vel gen handler User",
		"  vel gen handler Admin/Dashboard --resource",
		"  vel gen handler User --dir internal/web/handlers"); err != nil {
		return err
	}
	opts, err := parseGenHandlerArgs(args[1:])
	if err != nil {
		genHandlerUsage()
		return err
	}
	return console.MakeHandler(args[0], opts)
}

type genModelCmd struct{}

func (genModelCmd) name() string        { return "gen model" }
func (genModelCmd) description() string { return "Create a new model" }
func genModelUsage() {
	prism.Newline()
	prism.Muted("Usage: vel gen model [name]")
	prism.Newline()
	prism.Muted("Examples:")
	prism.Muted("  vel gen model User")
	prism.Muted("  vel gen model Post --uuid --soft-deletes")
	prism.Muted("  vel gen model Comment --migration")
	prism.Muted("  vel gen model Invoice --dir internal/billing/models")
}

func (genModelCmd) run(a *App, args []string) error {
	if err := requireGenName(args, "Model", "gen model",
		"  vel gen model User",
		"  vel gen model Post --uuid --soft-deletes",
		"  vel gen model Comment --migration",
		"  vel gen model Invoice --dir internal/billing/models"); err != nil {
		return err
	}
	opts, err := parseGenModelArgs(args[1:])
	if err != nil {
		genModelUsage()
		return err
	}
	return console.MakeModel(args[0], opts)
}

type genMigrationCmd struct{}

func (genMigrationCmd) name() string        { return "gen migration" }
func (genMigrationCmd) description() string { return "Create a new migration" }
func genMigrationUsage() {
	prism.Newline()
	prism.Muted("Usage: vel gen migration [name]")
	prism.Newline()
	prism.Muted("Examples:")
	prism.Muted("  vel gen migration create_posts")
	prism.Muted("  vel gen migration add_slug_to_posts --table=posts")
	prism.Muted("  vel gen migration create_comments --create=comments")
	prism.Muted("  vel gen migration create_posts --dir db/migrations")
}

func (genMigrationCmd) run(a *App, args []string) error {
	if err := requireGenName(args, "Migration", "gen migration",
		"  vel gen migration create_posts",
		"  vel gen migration add_slug_to_posts --table=posts",
		"  vel gen migration create_comments --create=comments",
		"  vel gen migration create_posts --dir db/migrations"); err != nil {
		return err
	}
	opts, err := parseGenMigrationArgs(args[1:])
	if err != nil {
		genMigrationUsage()
		return err
	}
	return console.MakeMigration(args[0], opts)
}

type genMiddlewareCmd struct{}

func (genMiddlewareCmd) name() string        { return "gen middleware" }
func (genMiddlewareCmd) description() string { return "Create a new middleware" }
func (genMiddlewareCmd) run(a *App, args []string) error {
	if err := requireGenName(args, "Middleware", "gen middleware"); err != nil {
		return err
	}
	dir, err := parseDirOnlyArgs(args[1:])
	if err != nil {
		genNameUsageHint("gen middleware")
		return err
	}
	return console.MakeMiddleware(args[0], console.MakeMiddlewareOptions{Dir: dir})
}

type genEventCmd struct{}

func (genEventCmd) name() string        { return "gen event" }
func (genEventCmd) description() string { return "Create a new event" }
func (genEventCmd) run(a *App, args []string) error {
	if err := requireGenName(args, "Event", "gen event"); err != nil {
		return err
	}
	dir, err := parseDirOnlyArgs(args[1:])
	if err != nil {
		genNameUsageHint("gen event")
		return err
	}
	return console.MakeEvent(args[0], console.MakeEventOptions{Dir: dir})
}

type genListenerCmd struct{}

func (genListenerCmd) name() string        { return "gen listener" }
func (genListenerCmd) description() string { return "Create a new listener" }
func (genListenerCmd) run(a *App, args []string) error {
	if err := requireGenName(args, "Listener", "gen listener"); err != nil {
		return err
	}
	dir, err := parseDirOnlyArgs(args[1:])
	if err != nil {
		genNameUsageHint("gen listener")
		return err
	}
	return console.MakeListener(args[0], console.MakeListenerOptions{Dir: dir})
}

type genJobCmd struct{}

func (genJobCmd) name() string        { return "gen job" }
func (genJobCmd) description() string { return "Create a new job" }
func (genJobCmd) run(a *App, args []string) error {
	if err := requireGenName(args, "Job", "gen job"); err != nil {
		return err
	}
	dir, err := parseDirOnlyArgs(args[1:])
	if err != nil {
		genNameUsageHint("gen job")
		return err
	}
	return console.MakeJob(args[0], console.MakeJobOptions{Dir: dir})
}

type genMailCmd struct{}

func (genMailCmd) name() string        { return "gen mail" }
func (genMailCmd) description() string { return "Create a new mailable" }
func (genMailCmd) run(a *App, args []string) error {
	if err := requireGenName(args, "Mail", "gen mail"); err != nil {
		return err
	}
	dir, err := parseDirOnlyArgs(args[1:])
	if err != nil {
		genNameUsageHint("gen mail")
		return err
	}
	return console.MakeMail(args[0], console.MakeMailOptions{Dir: dir})
}

type genNotificationCmd struct{}

func (genNotificationCmd) name() string        { return "gen notification" }
func (genNotificationCmd) description() string { return "Create a new notification" }
func (genNotificationCmd) run(a *App, args []string) error {
	if err := requireGenName(args, "Notification", "gen notification"); err != nil {
		return err
	}
	dir, err := parseDirOnlyArgs(args[1:])
	if err != nil {
		genNameUsageHint("gen notification")
		return err
	}
	return console.MakeNotification(args[0], console.MakeNotificationOptions{Dir: dir})
}

type genResourceCmd struct{}

func (genResourceCmd) name() string        { return "gen resource" }
func (genResourceCmd) description() string { return "Create a new API resource" }
func (genResourceCmd) run(a *App, args []string) error {
	if err := requireGenName(args, "Resource", "gen resource"); err != nil {
		return err
	}
	dir, err := parseDirOnlyArgs(args[1:])
	if err != nil {
		genNameUsageHint("gen resource")
		return err
	}
	return console.MakeResource(args[0], console.MakeResourceOptions{Dir: dir})
}

type genPolicyCmd struct{}

func (genPolicyCmd) name() string        { return "gen policy" }
func (genPolicyCmd) description() string { return "Create a new policy" }
func (genPolicyCmd) run(a *App, args []string) error {
	if err := requireGenName(args, "Policy", "gen policy"); err != nil {
		return err
	}
	dir, err := parseDirOnlyArgs(args[1:])
	if err != nil {
		genNameUsageHint("gen policy")
		return err
	}
	return console.MakePolicy(args[0], console.MakePolicyOptions{Dir: dir})
}

type genModuleCmd struct{}

func (genModuleCmd) name() string        { return "gen module" }
func (genModuleCmd) description() string { return "Create a new module" }
func (genModuleCmd) run(a *App, args []string) error {
	if err := requireGenName(args, "Module", "gen module"); err != nil {
		return err
	}
	dir, err := parseDirOnlyArgs(args[1:])
	if err != nil {
		genNameUsageHint("gen module")
		return err
	}
	return console.MakeModule(args[0], console.MakeModuleOptions{Dir: dir})
}

type genCommandCmd struct{}

func (genCommandCmd) name() string        { return "gen command" }
func (genCommandCmd) description() string { return "Create a new command" }
func (genCommandCmd) run(a *App, args []string) error {
	if err := requireGenName(args, "Command", "gen command"); err != nil {
		return err
	}
	dir, err := parseDirOnlyArgs(args[1:])
	if err != nil {
		genNameUsageHint("gen command")
		return err
	}
	return console.MakeCommand(args[0], console.MakeCommandOptions{Dir: dir})
}
