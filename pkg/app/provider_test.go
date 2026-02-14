package app

import (
	"context"
	"errors"
	"testing"
)

// testProvider records lifecycle calls for verification.
type testProvider struct {
	name        string
	calls       *[]string
	registerErr error
	bootErr     error
	shutdownErr error
}

func (p *testProvider) Register(_ *Services) error {
	*p.calls = append(*p.calls, p.name+":register")
	return p.registerErr
}

func (p *testProvider) Boot(_ *Services) error {
	*p.calls = append(*p.calls, p.name+":boot")
	return p.bootErr
}

func (p *testProvider) Shutdown(_ context.Context) error {
	*p.calls = append(*p.calls, p.name+":shutdown")
	return p.shutdownErr
}

func TestServiceProvider_RegisterThenBoot(t *testing.T) {
	var calls []string
	providers := []ServiceProvider{
		&testProvider{name: "A", calls: &calls},
		&testProvider{name: "B", calls: &calls},
		&testProvider{name: "C", calls: &calls},
	}

	s := &Services{}
	for _, p := range providers {
		if err := p.Register(s); err != nil {
			t.Fatalf("unexpected register error: %v", err)
		}
	}
	for _, p := range providers {
		if err := p.Boot(s); err != nil {
			t.Fatalf("unexpected boot error: %v", err)
		}
	}

	want := []string{
		"A:register", "B:register", "C:register",
		"A:boot", "B:boot", "C:boot",
	}
	if len(calls) != len(want) {
		t.Fatalf("got %d calls, want %d: %v", len(calls), len(want), calls)
	}
	for i, c := range calls {
		if c != want[i] {
			t.Errorf("call[%d] = %q, want %q", i, c, want[i])
		}
	}
}

func TestServiceProvider_AllRegisterBeforeBoot(t *testing.T) {
	var calls []string
	providers := []ServiceProvider{
		&testProvider{name: "A", calls: &calls},
		&testProvider{name: "B", calls: &calls},
	}

	s := &Services{}
	for _, p := range providers {
		_ = p.Register(s)
	}
	for _, p := range providers {
		_ = p.Boot(s)
	}

	// Verify no boot call appears before the last register call.
	lastRegister := -1
	firstBoot := -1
	for i, c := range calls {
		if c == "B:register" {
			lastRegister = i
		}
		if firstBoot == -1 && c == "A:boot" {
			firstBoot = i
		}
	}
	if firstBoot <= lastRegister {
		t.Errorf("boot started before all registers completed: calls = %v", calls)
	}
}

func TestServiceProvider_ShutdownReverseOrder(t *testing.T) {
	var calls []string
	providers := []ServiceProvider{
		&testProvider{name: "A", calls: &calls},
		&testProvider{name: "B", calls: &calls},
		&testProvider{name: "C", calls: &calls},
	}

	ctx := context.Background()
	for i := len(providers) - 1; i >= 0; i-- {
		_ = providers[i].Shutdown(ctx)
	}

	want := []string{"C:shutdown", "B:shutdown", "A:shutdown"}
	if len(calls) != len(want) {
		t.Fatalf("got %d calls, want %d: %v", len(calls), len(want), calls)
	}
	for i, c := range calls {
		if c != want[i] {
			t.Errorf("call[%d] = %q, want %q", i, c, want[i])
		}
	}
}

func TestServiceProvider_RegisterErrorPropagates(t *testing.T) {
	var calls []string
	wantErr := errors.New("register failed")
	providers := []ServiceProvider{
		&testProvider{name: "A", calls: &calls},
		&testProvider{name: "B", calls: &calls, registerErr: wantErr},
		&testProvider{name: "C", calls: &calls},
	}

	s := &Services{}
	var gotErr error
	for _, p := range providers {
		if err := p.Register(s); err != nil {
			gotErr = err
			break
		}
	}

	if !errors.Is(gotErr, wantErr) {
		t.Errorf("got error %v, want %v", gotErr, wantErr)
	}
	// C should never have been called
	for _, c := range calls {
		if c == "C:register" {
			t.Error("C:register should not have been called after B failed")
		}
	}
}

func TestServiceProvider_BootErrorPropagates(t *testing.T) {
	var calls []string
	wantErr := errors.New("boot failed")
	providers := []ServiceProvider{
		&testProvider{name: "A", calls: &calls},
		&testProvider{name: "B", calls: &calls, bootErr: wantErr},
	}

	s := &Services{}
	for _, p := range providers {
		_ = p.Register(s)
	}

	var gotErr error
	for _, p := range providers {
		if err := p.Boot(s); err != nil {
			gotErr = err
			break
		}
	}

	if !errors.Is(gotErr, wantErr) {
		t.Errorf("got error %v, want %v", gotErr, wantErr)
	}
}

func TestServiceProvider_NilShutdownDoesNotBreakChain(t *testing.T) {
	var calls []string
	providers := []ServiceProvider{
		&testProvider{name: "A", calls: &calls},
		&testProvider{name: "B", calls: &calls}, // returns nil
		&testProvider{name: "C", calls: &calls},
	}

	ctx := context.Background()
	var firstErr error
	for i := len(providers) - 1; i >= 0; i-- {
		if err := providers[i].Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	if firstErr != nil {
		t.Errorf("unexpected error: %v", firstErr)
	}

	want := []string{"C:shutdown", "B:shutdown", "A:shutdown"}
	if len(calls) != len(want) {
		t.Fatalf("got %d calls, want %d: %v", len(calls), len(want), calls)
	}
	for i, c := range calls {
		if c != want[i] {
			t.Errorf("call[%d] = %q, want %q", i, c, want[i])
		}
	}
}
