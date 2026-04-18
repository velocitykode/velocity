package chain

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/app"
)

// trackingProvider records lifecycle calls for verification.
type trackingProvider struct {
	name        string
	calls       *[]string
	registerErr error
	bootErr     error
	shutdownErr error
}

func (p *trackingProvider) Register(_ *app.Services) error {
	if p.calls != nil {
		*p.calls = append(*p.calls, p.name+":register")
	}
	return p.registerErr
}

func (p *trackingProvider) Boot(_ *app.Services) error {
	if p.calls != nil {
		*p.calls = append(*p.calls, p.name+":boot")
	}
	return p.bootErr
}

func (p *trackingProvider) Shutdown(_ context.Context) error {
	if p.calls != nil {
		*p.calls = append(*p.calls, p.name+":shutdown")
	}
	return p.shutdownErr
}

func TestProviderRegistry_AddSingle(t *testing.T) {
	var calls []string
	p := &trackingProvider{name: "A", calls: &calls}

	var reg ProviderRegistry
	reg.Add(p)

	if len(reg.providers) != 1 {
		t.Fatalf("got %d providers, want 1", len(reg.providers))
	}
}

func TestProviderRegistry_AddMultiple(t *testing.T) {
	var calls []string
	pA := &trackingProvider{name: "A", calls: &calls}
	pB := &trackingProvider{name: "B", calls: &calls}
	pC := &trackingProvider{name: "C", calls: &calls}

	var reg ProviderRegistry
	reg.Add(pA, pB, pC)

	if len(reg.providers) != 3 {
		t.Fatalf("got %d providers, want 3", len(reg.providers))
	}
}

func TestProviderRegistry_AddAccumulates(t *testing.T) {
	var calls []string
	pA := &trackingProvider{name: "A", calls: &calls}
	pB := &trackingProvider{name: "B", calls: &calls}

	var reg ProviderRegistry
	reg.Add(pA)
	reg.Add(pB)

	if len(reg.providers) != 2 {
		t.Fatalf("got %d providers, want 2", len(reg.providers))
	}
}

func TestProviderRegistry_ProvidersReturnsCopy(t *testing.T) {
	pA := &trackingProvider{name: "A"}
	pB := &trackingProvider{name: "B"}

	var reg ProviderRegistry
	reg.Add(pA, pB)

	got := reg.Providers()
	if len(got) != 2 {
		t.Fatalf("Providers() len = %d, want 2", len(got))
	}

	// Mutating the returned slice must not affect the registry.
	got[0] = nil
	if reg.providers[0] == nil {
		t.Error("mutating Providers() return value affected internal state")
	}
}
