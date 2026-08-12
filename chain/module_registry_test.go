package chain

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/app"
)

// trackingModule records lifecycle calls for verification.
type trackingModule struct {
	name        string
	calls       *[]string
	initErr     error
	startErr    error
	shutdownErr error
}

func (p *trackingModule) Init(_ *app.Services) error {
	if p.calls != nil {
		*p.calls = append(*p.calls, p.name+":init")
	}
	return p.initErr
}

func (p *trackingModule) Start(_ *app.Services) error {
	if p.calls != nil {
		*p.calls = append(*p.calls, p.name+":start")
	}
	return p.startErr
}

func (p *trackingModule) Shutdown(_ context.Context) error {
	if p.calls != nil {
		*p.calls = append(*p.calls, p.name+":shutdown")
	}
	return p.shutdownErr
}

func TestModuleRegistry_AddSingle(t *testing.T) {
	var calls []string
	p := &trackingModule{name: "A", calls: &calls}

	var reg ModuleRegistry
	reg.Add(p)

	if len(reg.modules) != 1 {
		t.Fatalf("got %d modules, want 1", len(reg.modules))
	}
}

func TestModuleRegistry_AddMultiple(t *testing.T) {
	var calls []string
	pA := &trackingModule{name: "A", calls: &calls}
	pB := &trackingModule{name: "B", calls: &calls}
	pC := &trackingModule{name: "C", calls: &calls}

	var reg ModuleRegistry
	reg.Add(pA, pB, pC)

	if len(reg.modules) != 3 {
		t.Fatalf("got %d modules, want 3", len(reg.modules))
	}
}

func TestModuleRegistry_AddAccumulates(t *testing.T) {
	var calls []string
	pA := &trackingModule{name: "A", calls: &calls}
	pB := &trackingModule{name: "B", calls: &calls}

	var reg ModuleRegistry
	reg.Add(pA)
	reg.Add(pB)

	if len(reg.modules) != 2 {
		t.Fatalf("got %d modules, want 2", len(reg.modules))
	}
}

func TestModuleRegistry_ModulesReturnsCopy(t *testing.T) {
	pA := &trackingModule{name: "A"}
	pB := &trackingModule{name: "B"}

	var reg ModuleRegistry
	reg.Add(pA, pB)

	got := reg.Modules()
	if len(got) != 2 {
		t.Fatalf("Modules() len = %d, want 2", len(got))
	}

	// Mutating the returned slice must not affect the registry.
	got[0] = nil
	if reg.modules[0] == nil {
		t.Error("mutating Modules() return value affected internal state")
	}
}
