package scheduler

import (
	"context"
	"sync"
)

var (
	globalScheduler *Scheduler
	globalMu        sync.RWMutex
	globalManager   *Manager
)

// init initializes the scheduler package.
// Use scheduler.New() and scheduler.NewManager() to create instances explicitly.
func init() {
	// No-op: global singleton is no longer eagerly initialized.
	// Scheduler instances should be created explicitly via New() and NewManager().
}

// Global scheduler functions

// GetScheduler returns the global scheduler.
func GetScheduler() *Scheduler {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalScheduler
}

// SetScheduler sets the global scheduler.
func SetScheduler(s *Scheduler) {
	globalMu.Lock()
	defer globalMu.Unlock()
	globalScheduler = s
}

// Call schedules a closure using the global scheduler.
func Call(callback func()) *Job {
	return globalScheduler.Call(callback)
}

// Command schedules a command using the global scheduler.
func Command(command string, args ...string) *Job {
	return globalScheduler.Command(command, args...)
}

// Run starts the global scheduler.
func Run(ctx context.Context) error {
	return globalScheduler.Run(ctx)
}

// Stop stops the global scheduler.
func Stop() {
	globalScheduler.Stop()
}

// Jobs returns all jobs from the global scheduler.
func Jobs() []*Job {
	return globalScheduler.Jobs()
}

// GetManager returns the global manager.
func GetManager() *Manager {
	return globalManager
}
