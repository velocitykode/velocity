package velocity

import (
	"context"
	"errors"
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
	*p.calls = append(*p.calls, p.name+":register")
	return p.initErr
}

func (p *trackingModule) Start(_ *app.Services) error {
	*p.calls = append(*p.calls, p.name+":boot")
	return p.startErr
}

func (p *trackingModule) Shutdown(_ context.Context) error {
	*p.calls = append(*p.calls, p.name+":shutdown")
	return p.shutdownErr
}

func TestNewTestApp_WithModules_Lifecycle(t *testing.T) {
	var calls []string
	pA := &trackingModule{name: "A", calls: &calls}
	pB := &trackingModule{name: "B", calls: &calls}

	a, err := NewTestApp(WithModules(pA, pB))
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

func TestNewTestApp_WithModules_InitError(t *testing.T) {
	var calls []string
	wantErr := errors.New("register boom")
	pA := &trackingModule{name: "A", calls: &calls}
	pB := &trackingModule{name: "B", calls: &calls, initErr: wantErr}
	pC := &trackingModule{name: "C", calls: &calls}

	_, err := NewTestApp(WithModules(pA, pB, pC))
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

func TestNewTestApp_WithModules_StartError(t *testing.T) {
	var calls []string
	wantErr := errors.New("boot boom")
	pA := &trackingModule{name: "A", calls: &calls}
	pB := &trackingModule{name: "B", calls: &calls, startErr: wantErr}

	_, err := NewTestApp(WithModules(pA, pB))
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

func TestNewTestApp_WithModules_ShutdownError(t *testing.T) {
	var calls []string
	shutdownErr := errors.New("shutdown boom")
	pA := &trackingModule{name: "A", calls: &calls}
	pB := &trackingModule{name: "B", calls: &calls, shutdownErr: shutdownErr}

	a, err := NewTestApp(WithModules(pA, pB))
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	calls = nil
	err = a.Shutdown(context.Background())
	if err == nil {
		t.Fatal("expected shutdown error to propagate")
	}
	if !errors.Is(err, shutdownErr) {
		t.Errorf("expected wrapped shutdown error, got: %v", err)
	}

	// Both modules should still have been called (first error captured, chain continues)
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
