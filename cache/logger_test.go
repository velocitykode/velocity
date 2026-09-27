package cache

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	logdrivers "github.com/velocitykode/velocity/log/drivers"
)

var _ contract.LoggerAware = (*Manager)(nil)

// loggerProbeStore records the StoreConfig.Logger its factory was given.
type loggerProbeStore struct {
	Store
	logger contract.Logger
}

// The manager hands its logger to the store config of every store it
// builds; a store config that sets its own keeps it.
func TestManager_HandsItsLoggerToStoreConfigs(t *testing.T) {
	const driver = "logger-probe"
	prev := Drivers().Override(driver, func(_ context.Context, cfg StoreConfig) (Store, error) {
		return &loggerProbeStore{logger: cfg.Logger}, nil
	})
	t.Cleanup(func() { Drivers().Override(driver, prev) })

	own := logdrivers.NewConsoleLoggerTo(&fallbacklogtest.Output{}, 0)
	managerLogger := logdrivers.NewConsoleLoggerTo(&fallbacklogtest.Output{}, 0)
	m := NewManager(&Config{
		Default: "a",
		Stores: map[string]StoreConfig{
			"a": {Driver: driver},
			"b": {Driver: driver, Logger: own},
		},
	})
	m.SetLogger(managerLogger)

	for name, want := range map[string]contract.Logger{"a": managerLogger, "b": own} {
		s, err := m.Store(name)
		if err != nil {
			t.Fatalf("Store(%q): %v", name, err)
		}
		if got := s.(*loggerProbeStore).logger; got != want {
			t.Errorf("store %q built with logger %p, want %p", name, got, want)
		}
	}
}

// SetLogger may run while stores are built: the logger is read under the
// manager's mutex.
func TestManager_SetLoggerWhileBuildingStoresIsSafe(t *testing.T) {
	stores := map[string]StoreConfig{}
	for i := 0; i < 64; i++ {
		stores[fmt.Sprintf("s%d", i)] = StoreConfig{Driver: DriverMemory}
	}
	m := NewManager(&Config{Default: "s0", Stores: stores})
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	out := &fallbacklogtest.Output{}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 64; i++ {
			m.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))
			m.SetLogger(nil)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 64; i++ {
			_, _ = m.Store(fmt.Sprintf("s%d", i))
		}
	}()
	wg.Wait()
}

// SetLogger's edge inputs: nil hands later stores no logger (they fall
// back); a zero-value Manager and a shut-down Manager take a logger without
// panicking.
func TestManager_SetLoggerEdgeInputs(t *testing.T) {
	const driver = "logger-probe-edge"
	prev := Drivers().Override(driver, func(_ context.Context, cfg StoreConfig) (Store, error) {
		return &loggerProbeStore{logger: cfg.Logger}, nil
	})
	t.Cleanup(func() { Drivers().Override(driver, prev) })

	t.Run("nil", func(t *testing.T) {
		m := NewManager(&Config{Default: "a", Stores: map[string]StoreConfig{"a": {Driver: driver}}})
		m.SetLogger(logdrivers.NewConsoleLoggerTo(&fallbacklogtest.Output{}, 0))
		m.SetLogger(nil)
		s, err := m.Store("a")
		if err != nil {
			t.Fatalf("Store: %v", err)
		}
		if got := s.(*loggerProbeStore).logger; got != nil {
			t.Errorf("store built with logger %v after SetLogger(nil), want nil", got)
		}
	})

	t.Run("zero value", func(t *testing.T) {
		var m Manager
		l := logdrivers.NewConsoleLoggerTo(&fallbacklogtest.Output{}, 0)
		m.SetLogger(l)
		m.mu.RLock()
		got := m.logger
		m.mu.RUnlock()
		if got != contract.Logger(l) {
			t.Errorf("logger = %v, want the installed one", got)
		}
	})

	t.Run("after shutdown", func(t *testing.T) {
		m := NewManager(&Config{Default: "a", Stores: map[string]StoreConfig{"a": {Driver: DriverMemory}}})
		if _, err := m.Store("a"); err != nil {
			t.Fatalf("Store: %v", err)
		}
		if err := m.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
		m.SetLogger(logdrivers.NewConsoleLoggerTo(&fallbacklogtest.Output{}, 0))
		m.SetLogger(nil)
	})
}
