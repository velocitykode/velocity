package scheduler

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/velocitykode/velocity/contract"
)

// TaskScheduler is the interface satisfied by *Scheduler. It covers the
// methods used through app.Services and router.Context for job scheduling
// and lifecycle management.
//
// Configuration methods that return *Scheduler for chaining (SetTimezone,
// SetLogger, MaintenanceMode, Before, After) are intentionally excluded --
// they are only called on the concrete type during bootstrap.
type TaskScheduler interface {
	Add(job *Job) *Job
	Call(callback func()) *Job
	Command(command string, args ...string) *Job
	Run(ctx context.Context) error
	Stop()
	Jobs() []*Job
	SetEventDispatcher(fn func(event interface{}) error)
	SetEnv(env string)
}

// Verify *Scheduler implements TaskScheduler at compile time.
var _ TaskScheduler = (*Scheduler)(nil)

// Scheduler manages and executes scheduled jobs
type Scheduler struct {
	mu              sync.RWMutex
	jobs            []*Job
	ticker          *time.Ticker
	stop            chan struct{}
	running         bool
	timezone        *time.Location
	maintenanceMode bool
	appEnv          string
	beforeCallbacks []func()
	afterCallbacks  []func()
	logger          Logger
	eventDispatcher func(event interface{}) error
}

// SetEventDispatcher sets the function used to dispatch events.
func (s *Scheduler) SetEventDispatcher(fn func(event interface{}) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eventDispatcher = fn
}

// dispatchEvent dispatches an event if a dispatcher is configured.
func (s *Scheduler) dispatchEvent(event interface{}) {
	if s.eventDispatcher != nil {
		s.eventDispatcher(event)
	}
}

// Logger interface for scheduler logging
type Logger interface {
	Info(msg string, keysAndValues ...interface{})
	Error(msg string, keysAndValues ...interface{})
	Debug(msg string, keysAndValues ...interface{})
}

// defaultLogger implements a simple logger
type defaultLogger struct{}

func (l *defaultLogger) Info(msg string, keysAndValues ...interface{}) {
	log.Print("[INFO] " + msg + fmtKVs(keysAndValues))
}
func (l *defaultLogger) Error(msg string, keysAndValues ...interface{}) {
	log.Print("[ERROR] " + msg + fmtKVs(keysAndValues))
}
func (l *defaultLogger) Debug(msg string, keysAndValues ...interface{}) {
	log.Print("[DEBUG] " + msg + fmtKVs(keysAndValues))
}

func fmtKVs(kvs []interface{}) string {
	if len(kvs) == 0 {
		return ""
	}
	s := ""
	for i := 0; i+1 < len(kvs); i += 2 {
		s += fmt.Sprintf(" %v=%v", kvs[i], kvs[i+1])
	}
	return s
}

// New creates a new scheduler instance
func New() *Scheduler {
	return &Scheduler{
		jobs:     make([]*Job, 0),
		stop:     make(chan struct{}),
		timezone: time.Local,
		logger:   &defaultLogger{},
	}
}

// SetEnv sets the application environment (e.g. "production", "staging") used by
// jobs with environment constraints. Called during app initialization.
func (s *Scheduler) SetEnv(env string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appEnv = env
}

// SetTimezone sets the timezone for the scheduler
func (s *Scheduler) SetTimezone(tz *time.Location) *Scheduler {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timezone = tz
	return s
}

// SetLogger sets a custom logger
func (s *Scheduler) SetLogger(logger Logger) *Scheduler {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logger = logger
	return s
}

// MaintenanceMode enables or disables maintenance mode
func (s *Scheduler) MaintenanceMode(enabled bool) *Scheduler {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maintenanceMode = enabled
	return s
}

// Before registers a callback to run before job execution
func (s *Scheduler) Before(callback func()) *Scheduler {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.beforeCallbacks = append(s.beforeCallbacks, callback)
	return s
}

// After registers a callback to run after job execution
func (s *Scheduler) After(callback func()) *Scheduler {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.afterCallbacks = append(s.afterCallbacks, callback)
	return s
}

// Add registers a new job with the scheduler. Multiple jobs with the same name
// may be added (append semantics -- duplicates are intentional, not an error).
// Panics with *contract.RegistrationError if job is nil.
func (s *Scheduler) Add(job *Job) *Job {
	if job == nil {
		panic(contract.NewRegistrationError("scheduler", "nil job"))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job.scheduler = s
	job.timezone = s.timezone
	s.jobs = append(s.jobs, job)
	return job
}

// Call creates a new job that executes a closure
func (s *Scheduler) Call(callback func()) *Job {
	job := &Job{
		name:     "closure",
		callback: callback,
		schedule: &Schedule{},
	}
	return s.Add(job)
}

// Command creates a new job that executes a command
func (s *Scheduler) Command(command string, args ...string) *Job {
	job := &Job{
		name:     command,
		command:  command,
		args:     args,
		schedule: &Schedule{},
	}
	return s.Add(job)
}

// Run starts the scheduler
func (s *Scheduler) Run(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = true
	s.ticker = time.NewTicker(1 * time.Minute) // Check every minute
	s.mu.Unlock()

	s.logger.Info("Scheduler started")

	// Run immediately on start
	s.runDueJobs()

	for {
		select {
		case <-ctx.Done():
			s.Stop()
			return ctx.Err()
		case <-s.stop:
			return nil
		case <-s.ticker.C:
			s.runDueJobs()
		}
	}
}

// Stop stops the scheduler
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return
	}

	s.running = false
	if s.ticker != nil {
		s.ticker.Stop()
	}
	close(s.stop)
	s.logger.Info("Scheduler stopped")
}

// runDueJobs executes all jobs that are due
func (s *Scheduler) runDueJobs() {
	s.mu.RLock()
	if s.maintenanceMode {
		s.mu.RUnlock()
		return
	}
	jobs := make([]*Job, len(s.jobs))
	copy(jobs, s.jobs)
	beforeCallbacks := s.beforeCallbacks
	afterCallbacks := s.afterCallbacks
	s.mu.RUnlock()

	now := time.Now().In(s.timezone)

	// Run before callbacks
	for _, callback := range beforeCallbacks {
		callback()
	}

	// Check and run each job
	var wg sync.WaitGroup
	for _, job := range jobs {
		if job.IsDue(now) && job.ShouldRun() {
			wg.Add(1)
			go func(j *Job) {
				defer wg.Done()
				s.logger.Debug("Running job", "name", j.name)
				j.Run()
			}(job)
		}
	}

	wg.Wait()

	// Run after callbacks
	for _, callback := range afterCallbacks {
		callback()
	}
}

// Jobs returns all registered jobs
func (s *Scheduler) Jobs() []*Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	jobs := make([]*Job, len(s.jobs))
	copy(jobs, s.jobs)
	return jobs
}
