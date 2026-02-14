package velocity

import (
	"context"
	"errors"
	"testing"

	"github.com/velocitykode/velocity/pkg/app"
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
	*p.calls = append(*p.calls, p.name+":register")
	return p.registerErr
}

func (p *trackingProvider) Boot(_ *app.Services) error {
	*p.calls = append(*p.calls, p.name+":boot")
	return p.bootErr
}

func (p *trackingProvider) Shutdown(_ context.Context) error {
	*p.calls = append(*p.calls, p.name+":shutdown")
	return p.shutdownErr
}

func TestNewTestApp_WithProviders_Lifecycle(t *testing.T) {
	var calls []string
	pA := &trackingProvider{name: "A", calls: &calls}
	pB := &trackingProvider{name: "B", calls: &calls}

	a, err := NewTestApp(WithProviders(pA, pB))
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	// Verify Register→Boot ordering: all registers before any boot
	want := []string{"A:register", "B:register", "A:boot", "B:boot"}
	if len(calls) != len(want) {
		t.Fatalf("got %d calls, want %d: %v", len(calls), len(want), calls)
	}
	for i, c := range calls {
		if c != want[i] {
			t.Errorf("call[%d] = %q, want %q", i, c, want[i])
		}
	}

	// Now test shutdown in reverse order
	calls = nil
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error: %v", err)
	}

	wantShutdown := []string{"B:shutdown", "A:shutdown"}
	if len(calls) != len(wantShutdown) {
		t.Fatalf("got %d shutdown calls, want %d: %v", len(calls), len(wantShutdown), calls)
	}
	for i, c := range calls {
		if c != wantShutdown[i] {
			t.Errorf("shutdown call[%d] = %q, want %q", i, c, wantShutdown[i])
		}
	}
}

func TestNewTestApp_WithProviders_RegisterError(t *testing.T) {
	var calls []string
	wantErr := errors.New("register boom")
	pA := &trackingProvider{name: "A", calls: &calls}
	pB := &trackingProvider{name: "B", calls: &calls, registerErr: wantErr}
	pC := &trackingProvider{name: "C", calls: &calls}

	_, err := NewTestApp(WithProviders(pA, pB, pC))
	if err == nil {
		t.Fatal("expected error from register")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("expected wrapped register error, got: %v", err)
	}

	// C should never have been called
	for _, c := range calls {
		if c == "C:register" {
			t.Error("C:register should not have been called after B failed")
		}
	}
	// No boot calls should have happened
	for _, c := range calls {
		if c == "A:boot" || c == "B:boot" || c == "C:boot" {
			t.Errorf("no boot should run after register failure, but got %q", c)
		}
	}
}

func TestNewTestApp_WithProviders_BootError(t *testing.T) {
	var calls []string
	wantErr := errors.New("boot boom")
	pA := &trackingProvider{name: "A", calls: &calls}
	pB := &trackingProvider{name: "B", calls: &calls, bootErr: wantErr}

	_, err := NewTestApp(WithProviders(pA, pB))
	if err == nil {
		t.Fatal("expected error from boot")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("expected wrapped boot error, got: %v", err)
	}

	// Both should have registered
	registered := 0
	for _, c := range calls {
		if c == "A:register" || c == "B:register" {
			registered++
		}
	}
	if registered != 2 {
		t.Errorf("expected 2 register calls, got %d", registered)
	}
}
