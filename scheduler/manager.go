package scheduler

import (
	"context"
	"log"
	"sync"

	"github.com/velocitykode/velocity/internal/panicerr"
)

// Manager manages multiple schedulers
type Manager struct {
	mu         sync.RWMutex
	schedulers map[string]*Scheduler
	default_   string
}

// NewManager creates a new scheduler manager
func NewManager() *Manager {
	return &Manager{
		schedulers: make(map[string]*Scheduler),
		default_:   "default",
	}
}

// Add adds a scheduler to the manager
func (m *Manager) Add(name string, scheduler *Scheduler) *Manager {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.schedulers[name] = scheduler
	return m
}

// Get retrieves a scheduler by name
func (m *Manager) Get(name string) (*Scheduler, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.schedulers[name]
	return s, ok
}

// Default returns the default scheduler
func (m *Manager) Default() *Scheduler {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if s, ok := m.schedulers[m.default_]; ok {
		return s
	}
	// Create default scheduler if it doesn't exist
	m.mu.RUnlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.schedulers[m.default_]; ok {
		return s
	}
	s := New()
	m.schedulers[m.default_] = s
	return s
}

// SetDefault sets the default scheduler name
func (m *Manager) SetDefault(name string) *Manager {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.default_ = name
	return m
}

// RunAll starts all schedulers
func (m *Manager) RunAll(ctx context.Context) error {
	m.mu.RLock()
	schedulers := make([]*Scheduler, 0, len(m.schedulers))
	for _, s := range m.schedulers {
		schedulers = append(schedulers, s)
	}
	m.mu.RUnlock()

	var wg sync.WaitGroup
	errChan := make(chan error, len(schedulers))

	for _, s := range schedulers {
		wg.Add(1)
		// Recover from panics inside Scheduler.Run so one sub-scheduler
		// crashing does not deadlock the fan-in on wg.Wait.
		go func(scheduler *Scheduler) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					err := panicerr.FromRecovered(r)
					log.Printf("velocity/scheduler: run panic recovered: %v", err)
					errChan <- err
				}
			}()
			if err := scheduler.Run(ctx); err != nil {
				errChan <- err
			}
		}(s)
	}

	// Recover from panics in wg.Wait so errChan always closes and the
	// reader select below is never starved.
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("velocity/scheduler: manager wait panic recovered: %v", panicerr.FromRecovered(r))
			}
			close(errChan)
		}()
		wg.Wait()
	}()

	// Return first error if any
	for err := range errChan {
		if err != nil {
			return err
		}
	}

	return nil
}

// StopAll stops all schedulers
func (m *Manager) StopAll() {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, s := range m.schedulers {
		s.Stop()
	}
}
