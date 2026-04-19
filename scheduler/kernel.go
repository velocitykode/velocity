package scheduler

import (
	"context"
	"sync"
)

// Kernel provides a convenient way to define scheduled tasks for an application
type Kernel struct {
	mu        sync.RWMutex
	scheduler *Scheduler
	booted    bool
}

// NewKernel creates a new scheduler kernel
func NewKernel() *Kernel {
	return &Kernel{
		scheduler: New(),
	}
}

// Schedule returns the scheduler for defining tasks
func (k *Kernel) Schedule() *Scheduler {
	return k.scheduler
}

// Define is where scheduled tasks should be registered
// This method should be overridden in the application
func (k *Kernel) Define() {
	// Override this method in your application to define scheduled tasks
	// Example:
	// k.Schedule().Command("cache:clear").Daily()
	// k.Schedule().Call(func() {
	//     // Task logic
	// }).EveryFiveMinutes()
}

// Boot initializes the kernel
func (k *Kernel) Boot() {
	k.mu.Lock()
	defer k.mu.Unlock()

	if !k.booted {
		k.Define()
		k.booted = true
	}
}

// Run starts the scheduler
func (k *Kernel) Run(ctx context.Context) error {
	k.Boot()
	return k.scheduler.Run(ctx)
}

// Jobs returns all scheduled jobs
func (k *Kernel) Jobs() []*Job {
	return k.scheduler.Jobs()
}
