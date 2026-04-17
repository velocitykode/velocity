package bus

import (
	"context"
	"fmt"
	"reflect"
	"sync"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/panicerr"
	"github.com/velocitykode/velocity/pipeline"
)

// CommandDispatching is fired before a command is handled.
type CommandDispatching struct {
	Context     context.Context
	CommandType string
}

func (e *CommandDispatching) Name() string { return "command.dispatching" }

// CommandCompleted is fired after a command is handled successfully.
type CommandCompleted struct {
	Context     context.Context
	CommandType string
}

func (e *CommandCompleted) Name() string { return "command.completed" }

// CommandFailed is fired when a command handler returns an error.
type CommandFailed struct {
	Context     context.Context
	CommandType string
	Error       string
}

func (e *CommandFailed) Name() string { return "command.failed" }

// CommandQueued is fired when a command is pushed to the queue.
type CommandQueued struct {
	Context     context.Context
	CommandType string
}

func (e *CommandQueued) Name() string { return "command.queued" }

// Dispatcher is the interface for dispatching commands.
type Dispatcher interface {
	Dispatch(cmd Command) error
	DispatchAsync(cmd Command) error
}

// Bus dispatches commands to their registered handlers through optional middleware.
type Bus struct {
	handlers      map[reflect.Type]any // type -> handler wrapper func(Command) error
	middleware    []pipeline.Stage[Command]
	queue         QueuePusher
	queueName     string
	dispatchEvent func(event any) error
	mu            sync.RWMutex
}

// New creates a new Bus instance.
func New() *Bus {
	return &Bus{
		handlers: make(map[reflect.Type]any),
	}
}

// Register registers a typed handler for a command type.
// This is a package-level function due to Go generics limitations on methods.
// Panics with *contract.RegistrationError if handler is nil or a handler for the
// same command type is already registered.
func Register[T any](b *Bus, handler Handler[T]) {
	if handler == nil {
		panic(contract.NewRegistrationError("bus", fmt.Sprintf("nil handler for command type %s", reflect.TypeFor[T]().String())))
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	cmdType := reflect.TypeFor[T]()
	if _, exists := b.handlers[cmdType]; exists {
		panic(contract.NewRegistrationError("bus", fmt.Sprintf("handler for command type %s already registered", cmdType.String())))
	}
	b.handlers[cmdType] = func(cmd Command) error {
		return handler(cmd.(T))
	}
}

// Through adds middleware stages to the bus pipeline.
func (b *Bus) Through(stages ...pipeline.Stage[Command]) *Bus {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.middleware = append(b.middleware, stages...)
	return b
}

// SetQueue sets the queue driver for async dispatch.
func (b *Bus) SetQueue(q QueuePusher) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.queue = q
}

// SetQueueName sets the default queue name for async dispatch.
func (b *Bus) SetQueueName(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.queueName = name
}

// SetEventDispatcher sets the event dispatcher function.
// This follows the same instance-based event pattern as other velocity packages.
func (b *Bus) SetEventDispatcher(fn func(event any) error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dispatchEvent = fn
}

// Dispatch dispatches a command synchronously through middleware to its handler.
func (b *Bus) Dispatch(cmd Command) error {
	b.mu.RLock()
	handler, middleware, dispatchEvent := b.resolveHandler(cmd), b.copyMiddleware(), b.dispatchEvent
	b.mu.RUnlock()

	if handler == nil {
		return fmt.Errorf("bus: no handler registered for %T", cmd)
	}

	cmdType := reflect.TypeOf(cmd).String()

	// Event dispatch errors are intentionally ignored — events are best-effort
	// and must not affect command execution flow.
	if dispatchEvent != nil {
		_ = dispatchEvent(&CommandDispatching{CommandType: cmdType})
	}

	var err error
	if len(middleware) > 0 {
		err = b.safeExecute(func() error {
			return pipeline.New[Command]().Send(cmd).Through(middleware...).Then(handler)
		})
	} else {
		err = b.safeExecute(func() error {
			return handler(cmd)
		})
	}

	if dispatchEvent != nil {
		if err != nil {
			_ = dispatchEvent(&CommandFailed{CommandType: cmdType, Error: err.Error()})
		} else {
			_ = dispatchEvent(&CommandCompleted{CommandType: cmdType})
		}
	}

	return err
}

// DispatchAsync wraps the command as a job and pushes it to the queue.
func (b *Bus) DispatchAsync(cmd Command) error {
	b.mu.RLock()
	q, queueName, dispatchEvent := b.queue, b.queueName, b.dispatchEvent
	b.mu.RUnlock()

	if q == nil {
		return fmt.Errorf("bus: queue not configured for async dispatch")
	}

	job := &commandJob{cmd: cmd, bus: b}

	var args []string
	if queueName != "" {
		args = []string{queueName}
	}

	if err := q.Push(job, args...); err != nil {
		return fmt.Errorf("bus: failed to push command to queue: %w", err)
	}

	cmdType := reflect.TypeOf(cmd).String()
	if dispatchEvent != nil {
		_ = dispatchEvent(&CommandQueued{CommandType: cmdType})
	}

	return nil
}

// safeExecute runs fn and converts any panic into a returned error.
func (b *Bus) safeExecute(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = panicerr.FromRecovered(r)
		}
	}()
	return fn()
}

// resolveHandler finds the handler for a command. Must be called under RLock.
func (b *Bus) resolveHandler(cmd Command) func(Command) error {
	cmdType := reflect.TypeOf(cmd)
	if h, ok := b.handlers[cmdType]; ok {
		return h.(func(Command) error)
	}

	if _, ok := cmd.(SelfHandling); ok {
		return func(c Command) error {
			return c.(SelfHandling).Handle()
		}
	}

	return nil
}

// copyMiddleware returns a snapshot of middleware. Must be called under RLock.
func (b *Bus) copyMiddleware() []pipeline.Stage[Command] {
	if len(b.middleware) == 0 {
		return nil
	}
	cp := make([]pipeline.Stage[Command], len(b.middleware))
	copy(cp, b.middleware)
	return cp
}

// commandJob wraps a command as a queue job for async dispatch.
type commandJob struct {
	cmd Command
	bus *Bus
}

func (j *commandJob) Handle() error {
	return j.bus.Dispatch(j.cmd)
}

func (j *commandJob) Failed(err error) {
	j.bus.mu.RLock()
	dispatchEvent := j.bus.dispatchEvent
	j.bus.mu.RUnlock()

	if dispatchEvent != nil {
		cmdType := reflect.TypeOf(j.cmd).String()
		// Event dispatch errors are intentionally ignored — event dispatch is
		// best-effort and must not interfere with queue worker error handling.
		_ = dispatchEvent(&CommandFailed{CommandType: cmdType, Error: err.Error()})
	}
}
