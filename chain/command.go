package chain

import (
	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
)

// Command is the interface that custom user commands must implement.
// Commands are registered via the Commands chain step and invoked with
// `vel run <name>`.
type Command interface {
	// Name returns the command name used to invoke it (e.g. "seed").
	Name() string

	// Description returns a short description shown in help output.
	Description() string

	// Handle executes the command logic with access to all application services.
	Handle(s *app.Services, args []string) error
}

// Commands is a registry for user-defined commands.
// It provides O(1) lookup by name and panics on duplicate or nil registration.
type Commands struct {
	commands map[string]Command
	ordered  []Command // preserves insertion order for help output
}

// NewCommands creates an empty Commands registry.
// Called from the root velocity package during bootstrap.
func NewCommands() *Commands {
	return &Commands{
		commands: make(map[string]Command),
	}
}

// Add registers one or more commands. It panics with a RegistrationError if
// any command is nil or has a duplicate name.
func (r *Commands) Add(cmds ...Command) {
	for _, cmd := range cmds {
		if cmd == nil {
			panic(contract.NewRegistrationError("commands", "cannot register nil command"))
		}
		name := cmd.Name()
		if _, exists := r.commands[name]; exists {
			panic(contract.NewRegistrationError("commands", "duplicate command name: "+name))
		}
		r.commands[name] = cmd
		r.ordered = append(r.ordered, cmd)
	}
}

// Get returns the command with the given name and true, or nil and false.
func (r *Commands) Get(name string) (Command, bool) {
	cmd, ok := r.commands[name]
	return cmd, ok
}

// All returns all registered commands in insertion order.
func (r *Commands) All() []Command {
	result := make([]Command, len(r.ordered))
	copy(result, r.ordered)
	return result
}
