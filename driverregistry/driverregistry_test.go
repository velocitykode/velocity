package driverregistry

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// fakeDriver is the concrete instance type used by these tests. Pointer
// receiver methods cover the optional Closer / HealthChecker interfaces.
type fakeDriver struct {
	name      string
	closed    atomic.Bool
	healthErr error
}

func (f *fakeDriver) Close(_ context.Context) error {
	f.closed.Store(true)
	return nil
}

func (f *fakeDriver) Health(_ context.Context) error {
	return f.healthErr
}

type fakeConfig struct {
	Name string
}

func newReg(t *testing.T) *Registry[*fakeDriver, fakeConfig] {
	t.Helper()
	return New[*fakeDriver, fakeConfig]("test")
}

func TestRegisterAndResolve(t *testing.T) {
	r := newReg(t)
	r.Register("memory", func(_ context.Context, cfg fakeConfig) (*fakeDriver, error) {
		return &fakeDriver{name: cfg.Name}, nil
	})

	got, err := r.Resolve(context.Background(), "memory", fakeConfig{Name: "x"})
	if err != nil {
		t.Fatalf("Resolve: unexpected error %v", err)
	}
	if got.name != "x" {
		t.Fatalf("factory not invoked with cfg: got %q", got.name)
	}
}

func TestResolve_CaseInsensitive(t *testing.T) {
	r := newReg(t)
	r.Register("Redis", func(_ context.Context, _ fakeConfig) (*fakeDriver, error) {
		return &fakeDriver{name: "ok"}, nil
	})
	if _, err := r.Resolve(context.Background(), "REDIS", fakeConfig{}); err != nil {
		t.Fatalf("expected case-insensitive lookup, got %v", err)
	}
	if _, err := r.Resolve(context.Background(), "  redis  ", fakeConfig{}); err != nil {
		t.Fatalf("expected trimmed lookup, got %v", err)
	}
}

func TestResolve_NotFound(t *testing.T) {
	r := newReg(t)
	r.Register("memory", func(_ context.Context, _ fakeConfig) (*fakeDriver, error) {
		return &fakeDriver{}, nil
	})

	_, err := r.Resolve(context.Background(), "redis", fakeConfig{})
	if err == nil {
		t.Fatal("expected error for unknown driver")
	}
	if !errors.Is(err, ErrDriverNotFound) {
		t.Fatalf("expected errors.Is(ErrDriverNotFound), got %v", err)
	}
	var nfe *NotFoundError
	if !errors.As(err, &nfe) {
		t.Fatalf("expected *NotFoundError, got %T", err)
	}
	if nfe.Subsystem != "test" || nfe.Name != "redis" {
		t.Fatalf("NotFoundError fields wrong: %+v", nfe)
	}
	if len(nfe.Available) != 1 || nfe.Available[0] != "memory" {
		t.Fatalf("Available not populated: %+v", nfe.Available)
	}
}

func TestResolve_EmptyName(t *testing.T) {
	r := newReg(t)
	_, err := r.Resolve(context.Background(), "", fakeConfig{})
	if !errors.Is(err, ErrDriverNotFound) {
		t.Fatalf("expected ErrDriverNotFound for empty name, got %v", err)
	}
}

func TestRegister_PanicsOnDuplicate(t *testing.T) {
	r := newReg(t)
	r.Register("memory", func(_ context.Context, _ fakeConfig) (*fakeDriver, error) {
		return &fakeDriver{}, nil
	})
	defer func() {
		v := recover()
		if v == nil {
			t.Fatal("expected panic on duplicate registration")
		}
		regErr, ok := v.(*contract.RegistrationError)
		if !ok {
			t.Fatalf("expected *contract.RegistrationError, got %T", v)
		}
		if regErr.Package != "test" {
			t.Fatalf("expected subsystem 'test' in error, got %q", regErr.Package)
		}
	}()
	r.Register("memory", func(_ context.Context, _ fakeConfig) (*fakeDriver, error) {
		return &fakeDriver{}, nil
	})
}

func TestRegister_PanicsOnNilFactory(t *testing.T) {
	r := newReg(t)
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for nil factory")
		}
	}()
	r.Register("memory", nil)
}

func TestRegister_PanicsOnEmptyName(t *testing.T) {
	r := newReg(t)
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for empty name")
		}
	}()
	r.Register("   ", func(_ context.Context, _ fakeConfig) (*fakeDriver, error) {
		return &fakeDriver{}, nil
	})
}

func TestOverride(t *testing.T) {
	r := newReg(t)
	r.Register("memory", func(_ context.Context, _ fakeConfig) (*fakeDriver, error) {
		return &fakeDriver{name: "real"}, nil
	})

	prev := r.Override("memory", func(_ context.Context, _ fakeConfig) (*fakeDriver, error) {
		return &fakeDriver{name: "fake"}, nil
	})
	if prev == nil {
		t.Fatal("expected previous factory to be returned")
	}

	got, _ := r.Resolve(context.Background(), "memory", fakeConfig{})
	if got.name != "fake" {
		t.Fatalf("Override did not replace factory: %q", got.name)
	}

	// Restore via Override(nil) deletes; Override with prev re-installs.
	r.Override("memory", prev)
	got, _ = r.Resolve(context.Background(), "memory", fakeConfig{})
	if got.name != "real" {
		t.Fatalf("Override did not restore previous factory: %q", got.name)
	}

	// Override with nil deletes.
	r.Override("memory", nil)
	if r.Has("memory") {
		t.Fatal("Override(nil) should have deleted the entry")
	}
}

func TestHasAndNames(t *testing.T) {
	r := newReg(t)
	r.Register("memory", func(_ context.Context, _ fakeConfig) (*fakeDriver, error) {
		return &fakeDriver{}, nil
	})
	r.Register("redis", func(_ context.Context, _ fakeConfig) (*fakeDriver, error) {
		return &fakeDriver{}, nil
	})

	if !r.Has("memory") || !r.Has("REDIS") {
		t.Fatal("Has should be case-insensitive and report registered drivers")
	}
	if r.Has("file") {
		t.Fatal("Has should report false for unregistered drivers")
	}

	names := r.Names()
	if len(names) != 2 || names[0] != "memory" || names[1] != "redis" {
		t.Fatalf("Names should return sorted snapshot, got %v", names)
	}
}

func TestFactoryError_Propagates(t *testing.T) {
	r := newReg(t)
	sentinel := errors.New("connect refused")
	r.Register("redis", func(_ context.Context, _ fakeConfig) (*fakeDriver, error) {
		return nil, sentinel
	})
	_, err := r.Resolve(context.Background(), "redis", fakeConfig{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error to propagate, got %v", err)
	}
}

func TestResolve_PassesContext(t *testing.T) {
	r := newReg(t)
	type ctxKey struct{}
	r.Register("memory", func(ctx context.Context, _ fakeConfig) (*fakeDriver, error) {
		v, _ := ctx.Value(ctxKey{}).(string)
		return &fakeDriver{name: v}, nil
	})
	ctx := context.WithValue(context.Background(), ctxKey{}, "ctx-value")
	got, err := r.Resolve(ctx, "memory", fakeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got.name != "ctx-value" {
		t.Fatalf("ctx not threaded into factory: %q", got.name)
	}
}

func TestConcurrentRegisterResolve(t *testing.T) {
	r := newReg(t)
	const writers, readers = 8, 32
	var wg sync.WaitGroup

	wg.Add(writers)
	for i := 0; i < writers; i++ {
		i := i
		go func() {
			defer wg.Done()
			name := []string{"a", "b", "c", "d", "e", "f", "g", "h"}[i]
			r.Register(name, func(_ context.Context, _ fakeConfig) (*fakeDriver, error) {
				return &fakeDriver{name: name}, nil
			})
		}()
	}

	wg.Add(readers)
	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			// Best-effort lookups; we only care that Has / Resolve don't race.
			_ = r.Has("a")
			_, _ = r.Resolve(context.Background(), "b", fakeConfig{})
			_ = r.Names()
		}()
	}

	wg.Wait()
	// Sanity: at least one of the writers must have landed.
	if len(r.Names()) == 0 {
		t.Fatal("expected some registrations to land")
	}
}

// CloserContract documents the optional Closer interface contract by
// asserting a fake driver satisfies it at compile time.
func TestCloserContract(t *testing.T) {
	var _ Closer = (*fakeDriver)(nil)
	d := &fakeDriver{}
	if err := d.Close(context.Background()); err != nil {
		t.Fatalf("Close should be no-op: %v", err)
	}
	if !d.closed.Load() {
		t.Fatal("Close did not flip closed flag")
	}
}

func TestHealthCheckerContract(t *testing.T) {
	var _ HealthChecker = (*fakeDriver)(nil)
	sentinel := errors.New("unreachable")
	d := &fakeDriver{healthErr: sentinel}
	if err := d.Health(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("Health should propagate stored error, got %v", err)
	}
}
