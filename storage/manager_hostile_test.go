package storage

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
)

var hostileDriverSeq atomic.Int64

// A registered disk driver factory is user code. For every manager entry
// point it may call back into, a factory that panics, blocks, or makes
// that call must not deadlock ConfigureWithContext, and the manager works
// afterwards.
func TestManager_ConfigureWithHostileFactory(t *testing.T) {
	entries := map[string]func(m *Manager){
		"Disk":       func(m *Manager) { _, _ = m.Disk("main") },
		"Default":    func(m *Manager) { _, _ = m.Default() },
		"AddDisk":    func(m *Manager) { m.AddDisk("extra", NewMemoryDriver(DiskConfig{})) },
		"SetDefault": func(m *Manager) { _ = m.SetDefault("main") },
		"Shutdown":   func(m *Manager) { _ = m.Shutdown(context.Background()) },
	}
	for _, mode := range hostile.Modes() {
		for name, entry := range entries {
			t.Run(mode.String()+"/"+name, func(t *testing.T) {
				m := NewManager(Config{})
				code := hostile.New(t, mode, func() { entry(m) })
				driver := "hostile-" + strings.ReplaceAll(t.Name(), "/", "-") + string(rune('a'+hostileDriverSeq.Add(1)%26))
				drivers.Register(driver, func(_ context.Context, cfg DiskConfig) (Driver, error) {
					code.Run()
					return NewMemoryDriver(cfg), nil
				})
				t.Cleanup(func() { drivers.Override(driver, nil) })
				cfg := Config{Default: "main", Disks: map[string]DiskConfig{"main": {Driver: driver}}}
				configure := func() { _ = m.Configure(cfg) }
				if mode == hostile.Block {
					go configure()
					<-code.Entered()
					hostile.Within(t, hostile.Deadline, func() { entry(m) })
				} else {
					hostile.Within(t, hostile.Deadline, configure)
				}
				code.Release()
				code.Disarm()
				hostile.Within(t, hostile.Deadline, func() {
					if err := m.Configure(cfg); err != nil {
						t.Errorf("Configure after a retry: %v", err)
					}
					if _, err := m.Default(); err != nil {
						t.Errorf("Default after a retry: %v", err)
					}
				})
			})
		}
	}
}
