package velocity

import (
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/chain"
	"github.com/velocitykode/velocity/contract"
)

// stubCommand is a test implementation of the chain.Command interface.
type stubCommand struct {
	name        string
	description string
	handleFn    func(s *app.Services, args []string) error
}

func (c *stubCommand) Name() string        { return c.name }
func (c *stubCommand) Description() string { return c.description }
func (c *stubCommand) Handle(s *app.Services, args []string) error {
	if c.handleFn != nil {
		return c.handleFn(s, args)
	}
	return nil
}

func TestCommands_Add_Valid(t *testing.T) {
	cmds := chain.NewCommands()
	cmd := &stubCommand{name: "seed", description: "Seed the database"}

	cmds.Add(cmd)

	got, ok := cmds.Get("seed")
	if !ok {
		t.Fatal("expected command to be found")
	}
	if got != cmd {
		t.Error("returned command does not match registered command")
	}
}

func TestCommands_Add_Multiple(t *testing.T) {
	cmds := chain.NewCommands()
	cmdA := &stubCommand{name: "seed", description: "Seed the database"}
	cmdB := &stubCommand{name: "cleanup", description: "Clean up old data"}

	cmds.Add(cmdA, cmdB)

	if _, ok := cmds.Get("seed"); !ok {
		t.Error("expected 'seed' to be found")
	}
	if _, ok := cmds.Get("cleanup"); !ok {
		t.Error("expected 'cleanup' to be found")
	}
}

func TestCommands_Add_PanicsOnNil(t *testing.T) {
	cmds := chain.NewCommands()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on nil command")
		}
		regErr, ok := r.(*contract.RegistrationError)
		if !ok {
			t.Fatalf("expected *contract.RegistrationError, got %T", r)
		}
		if regErr.Package != "commands" {
			t.Errorf("expected package 'commands', got %q", regErr.Package)
		}
	}()

	cmds.Add(nil)
}

func TestCommands_Add_PanicsOnDuplicate(t *testing.T) {
	cmds := chain.NewCommands()
	cmdA := &stubCommand{name: "seed", description: "Seed v1"}
	cmdB := &stubCommand{name: "seed", description: "Seed v2"}

	cmds.Add(cmdA)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on duplicate name")
		}
		regErr, ok := r.(*contract.RegistrationError)
		if !ok {
			t.Fatalf("expected *contract.RegistrationError, got %T", r)
		}
		if regErr.Package != "commands" {
			t.Errorf("expected package 'commands', got %q", regErr.Package)
		}
	}()

	cmds.Add(cmdB)
}

func TestCommands_Get_NotFound(t *testing.T) {
	cmds := chain.NewCommands()

	_, ok := cmds.Get("nonexistent")
	if ok {
		t.Error("expected false for unknown command")
	}
}

func TestCommands_All_ReturnsAll(t *testing.T) {
	cmds := chain.NewCommands()
	cmdA := &stubCommand{name: "seed", description: "Seed the database"}
	cmdB := &stubCommand{name: "cleanup", description: "Clean up old data"}

	cmds.Add(cmdA, cmdB)

	all := cmds.All()
	if len(all) != 2 {
		t.Fatalf("got %d commands, want 2", len(all))
	}
	// Verify insertion order
	if all[0].Name() != "seed" {
		t.Errorf("all[0].Name() = %q, want %q", all[0].Name(), "seed")
	}
	if all[1].Name() != "cleanup" {
		t.Errorf("all[1].Name() = %q, want %q", all[1].Name(), "cleanup")
	}
}

func TestCommands_All_ReturnsCopy(t *testing.T) {
	cmds := chain.NewCommands()
	cmds.Add(&stubCommand{name: "seed", description: "Seed"})

	all := cmds.All()
	all[0] = nil // mutate the returned slice

	// Original should be unaffected
	got, ok := cmds.Get("seed")
	if !ok || got == nil {
		t.Error("mutating All() return value affected internal state")
	}
}

func TestCommands_ChainStep(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	got := a.Commands(func(r *chain.Commands) {})
	if got != a {
		t.Error("Commands() did not return same *App")
	}
}

func TestCommands_CalledDuringBootstrap(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	var called bool
	a.Commands(func(r *chain.Commands) {
		called = true
		r.Add(&stubCommand{name: "seed", description: "Seed the database"})
	})

	if err := a.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap() error: %v", err)
	}

	if !called {
		t.Error("Commands callback was not called during bootstrap")
	}

	// Verify the command is accessible
	cmd, ok := a.commands.Get("seed")
	if !ok {
		t.Fatal("expected command to be registered after bootstrap")
	}
	if cmd.Name() != "seed" {
		t.Errorf("cmd.Name() = %q, want %q", cmd.Name(), "seed")
	}
}

// commandTrackingProvider is a provider that implements chain.CommandProvider.
type commandTrackingProvider struct {
	trackingProvider
	commandsCalled bool
}

func (p *commandTrackingProvider) Commands(r *chain.Commands) {
	*p.calls = append(*p.calls, p.name+":commands")
	p.commandsCalled = true
	r.Add(&stubCommand{name: "provider-cmd", description: "From provider"})
}

func TestBootstrap_CommandProvider(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	var calls []string
	pA := &commandTrackingProvider{trackingProvider: trackingProvider{name: "A", calls: &calls}}

	a.Providers(func(r *chain.ProviderRegistry) {
		r.Add(pA)
	})

	if err := a.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap() error: %v", err)
	}

	if !pA.commandsCalled {
		t.Error("CommandProvider.Commands() was not called")
	}

	cmd, ok := a.commands.Get("provider-cmd")
	if !ok {
		t.Fatal("expected command from provider to be registered")
	}
	if cmd.Description() != "From provider" {
		t.Errorf("cmd.Description() = %q, want %q", cmd.Description(), "From provider")
	}
}

func TestBootstrap_CommandsOrder(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	var order []string

	a.Routes(func(r *chain.Routing) {
		order = append(order, "routes")
	}).Commands(func(r *chain.Commands) {
		order = append(order, "commands")
	})

	if err := a.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap() error: %v", err)
	}

	// Commands should come after routes
	if len(order) != 2 {
		t.Fatalf("got %d calls, want 2: %v", len(order), order)
	}
	if order[0] != "routes" {
		t.Errorf("order[0] = %q, want %q", order[0], "routes")
	}
	if order[1] != "commands" {
		t.Errorf("order[1] = %q, want %q", order[1], "commands")
	}
}

func TestCommands_HandleReceivesArgs(t *testing.T) {
	cmds := chain.NewCommands()
	var receivedArgs []string

	cmds.Add(&stubCommand{
		name:        "greet",
		description: "Greet someone",
		handleFn: func(s *app.Services, args []string) error {
			receivedArgs = args
			return nil
		},
	})

	cmd, _ := cmds.Get("greet")
	if err := cmd.Handle(nil, []string{"hello", "world"}); err != nil {
		t.Fatalf("Handle() error: %v", err)
	}

	if len(receivedArgs) != 2 || receivedArgs[0] != "hello" || receivedArgs[1] != "world" {
		t.Errorf("args = %v, want [hello world]", receivedArgs)
	}
}
