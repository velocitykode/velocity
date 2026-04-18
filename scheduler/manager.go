package scheduler

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/velocitykode/velocity/internal/panicerr"
)

// Manager manages multiple schedulers
type Manager struct {
	mu         sync.RWMutex
	schedulers map[string]*Scheduler
	default_   string

	// logger is stored atomically so recover() paths in RunAll can read
	// it without acquiring m.mu (some callers may already hold it).
	logger atomic.Value // holds mgrLoggerHolder{Logger}
}

// mgrLoggerHolder wraps a Logger so atomic.Value stores a single type.
type mgrLoggerHolder struct{ Logger }

// NewManager creates a new scheduler manager
func NewManager() *Manager {
	return &Manager{
		schedulers: make(map[string]*Scheduler),
		default_:   "default",
	}
}

// SetLogger installs a logger for manager-level events (recovered panics
// from individual schedulers running under RunAll, wait panics). The same
// logger is also propagated to every Scheduler the Manager owns so child
// schedulers log through the same pipeline. Nil disables logging.
func (m *Manager) SetLogger(l Logger) {
	m.logger.Store(mgrLoggerHolder{Logger: l})

	m.mu.RLock()
	schedulers := make([]*Scheduler, 0, len(m.schedulers))
	for _, s := range m.schedulers {
		schedulers = append(schedulers, s)
	}
	m.mu.RUnlock()

	for _, s := range schedulers {
		s.SetLogger(l)
	}
}

// log returns the installed logger, or nil when SetLogger has not been called.
func (m *Manager) log() Logger {
	v := m.logger.Load()
	if v == nil {
		return nil
	}
	return v.(mgrLoggerHolder).Logger
}

// logError emits an error event when a logger is configured.
func (m *Manager) logError(msg string, kvs ...any) {
	if l := m.log(); l != nil {
		l.Error(msg, kvs...)
	}
}

// Add adds a scheduler to the manager
func (m *Manager) Add(name string, scheduler *Scheduler) *Manager {
	m.mu.Lock()
	m.schedulers[name] = scheduler
	m.mu.Unlock()

	if l := m.log(); l != nil {
		scheduler.SetLogger(l)
	}
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
	if s, ok := m.schedulers[m.default_]; ok {
		m.mu.RUnlock()
		return s
	}
	// Create default scheduler if it doesn't exist
	m.mu.RUnlock()
	m.mu.Lock()
	if s, ok := m.schedulers[m.default_]; ok {
		m.mu.Unlock()
		return s
	}
	s := New()
	m.schedulers[m.default_] = s
	m.mu.Unlock()

	if l := m.log(); l != nil {
		s.SetLogger(l)
	}
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
					m.logError("velocity/scheduler: run panic recovered", "error", err)
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
				m.logError("velocity/scheduler: manager wait panic recovered", "error", panicerr.FromRecovered(r))
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
