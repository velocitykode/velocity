package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/panicerr"
	"github.com/velocitykode/velocity/pipeline"
	"github.com/velocitykode/velocity/queue"
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

// New creates a new Bus instance. The newly created bus is also installed as
// the package-level default bus so cross-process queue workers that hydrate a
// commandJob from JSON bytes have a bus to dispatch through. Apps that need
// to coordinate multiple buses should call SetDefaultBus explicitly after
// construction.
func New() *Bus {
	b := &Bus{
		handlers: make(map[reflect.Type]any),
	}
	SetDefaultBus(b)
	return b
}

// Register registers a typed handler for a command type.
// This is a package-level function due to Go generics limitations on methods.
// Panics with *contract.RegistrationError if handler is nil, a handler for the
// same command type is already registered, or the command type cannot be
// JSON-marshalled (required for async dispatch over queues).
//
// Register also installs a package-level factory keyed on the command's type
// name so cross-process queue workers can rehydrate the command from the
// serialized commandJob payload. Without that factory, DispatchAsync against
// a durable driver (Redis or database) would silently fall through to
// ErrJobNotFound on the consumer side.
func Register[T any](b *Bus, handler Handler[T]) {
	if handler == nil {
		panic(contract.NewRegistrationError("bus", fmt.Sprintf("nil handler for command type %s", reflect.TypeFor[T]().String())))
	}

	// Probe serializability at registration time so async dispatch cannot fail
	// at runtime with an obscure marshal error. We construct a zero value of T
	// and round-trip it through encoding/json.
	var zero T
	if _, err := json.Marshal(zero); err != nil {
		panic(contract.NewRegistrationError("bus", fmt.Sprintf("command type %s is not json-serializable: %v", reflect.TypeFor[T]().String(), err)))
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	cmdType := reflect.TypeFor[T]()
	if _, exists := b.handlers[cmdType]; exists {
		panic(contract.NewRegistrationError("bus", fmt.Sprintf("handler for command type %s already registered", cmdType.String())))
	}
	b.handlers[cmdType] = func(cmd Command) error {
		typed, ok := cmd.(T)
		if !ok {
			return fmt.Errorf("velocity/bus: command type mismatch: got %T, want %s", cmd, cmdType.String())
		}
		return handler(typed)
	}

	// Install a package-level factory so a queue worker (possibly in another
	// process) can rebuild the concrete command value from the serialized
	// commandJob payload. The key is the same string reflect.TypeOf(cmd)
	// produces on the producer side, so producer and consumer sides agree by
	// construction.
	registerCommandFactory(cmdType, func() Command {
		var v T
		return v
	})
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

	// Event dispatch errors are intentionally ignored, events are best-effort
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
//
// The command must be JSON-marshallable and must have been registered via
// Register[T] (or have a manually-installed factory). DispatchAsync refuses
// to enqueue otherwise so the silent-drop hole on durable drivers (Redis,
// database) is closed at the producer side. With the factory in place the
// consumer process can rehydrate the command from the persisted commandJob
// payload and call Bus.Dispatch against the locally-installed default bus.
func (b *Bus) DispatchAsync(cmd Command) error {
	b.mu.RLock()
	q, queueName, dispatchEvent := b.queue, b.queueName, b.dispatchEvent
	b.mu.RUnlock()

	if q == nil {
		return fmt.Errorf("bus: queue not configured for async dispatch")
	}

	cmdType := reflect.TypeOf(cmd)
	if cmdType == nil {
		return fmt.Errorf("bus: cannot dispatch nil command")
	}

	// Refuse to enqueue when no factory is registered. The consumer side
	// would otherwise return ErrJobNotFound or, worse, unmarshal into a
	// zero-value commandJob and silently drop the work. We surface the
	// configuration error here so a missing Register call is caught at
	// dispatch time rather than discovered by the user when a job vanishes.
	if _, ok := lookupCommandFactory(cmdType); !ok {
		return fmt.Errorf("velocity/bus: refusing to async-dispatch command %s: no factory registered (call bus.Register before DispatchAsync)", cmdType.String())
	}

	data, err := json.Marshal(cmd)
	if err != nil {
		return fmt.Errorf("velocity/bus: failed to marshal command %s: %w", cmdType.String(), err)
	}

	job := &commandJob{
		Type:    cmdType.String(),
		Data:    data,
		cmd:     cmd,
		bus:     b,
		cmdType: cmdType,
	}

	var args []string
	if queueName != "" {
		args = []string{queueName}
	}

	if err := q.Push(job, args...); err != nil {
		return fmt.Errorf("bus: failed to push command to queue: %w", err)
	}

	if dispatchEvent != nil {
		_ = dispatchEvent(&CommandQueued{CommandType: cmdType.String()})
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
//
// Wire format: Type is the reflect.TypeOf(cmd).String() identifier registered
// via Register[T]; Data is the JSON-encoded command bytes. Both round-trip
// through encoding/json so durable drivers (Redis, database) can persist the
// payload and rehydrate the concrete command on any worker, in any process.
//
// The unexported cmd/bus/cmdType fields are an in-process fast path. The
// memory queue driver retains the live pointer across a same-process Pop so
// the worker can dispatch without consulting the package-level factory
// registry. Cross-process pops always have cmd == nil after json.Unmarshal
// and Handle reconstructs the value from Type + Data via the registry.
type commandJob struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`

	// cmd, bus, cmdType are populated on the producer side for the in-process
	// fast path. They are never sent on the wire.
	cmd     Command
	bus     *Bus
	cmdType reflect.Type
}

// Handle dispatches the wrapped command. When the in-process fast-path
// fields are populated (memory driver, same process as the producer),
// Handle dispatches the live command pointer directly. When the fields
// are nil (cross-process worker), Handle reconstructs the command from
// Type + Data via the package-level factory registry and dispatches it
// through the default bus.
func (j *commandJob) Handle() error {
	cmd, b, err := j.resolveCommand()
	if err != nil {
		return err
	}
	if j.cmdType != nil && reflect.TypeOf(cmd) != j.cmdType {
		return fmt.Errorf("velocity/bus: command type mismatch in queued job: got %T, want %s", cmd, j.cmdType.String())
	}
	return b.Dispatch(cmd)
}

// resolveCommand recovers the concrete command value and the bus to dispatch
// through. The in-process producer-side pointer is preferred when set;
// otherwise the command is rebuilt from Type + Data via the package registry
// and dispatched against the default bus.
//
// A missing factory or missing default bus is a real error and surfaces to
// the worker so the job can be routed to failed_jobs / events. Returning a
// nil command with a nil error would reopen the silent-drop hole this fix
// closes.
func (j *commandJob) resolveCommand() (Command, *Bus, error) {
	if j.cmd != nil && j.bus != nil {
		return j.cmd, j.bus, nil
	}

	if j.Type == "" {
		return nil, nil, fmt.Errorf("velocity/bus: commandJob payload missing type")
	}

	factory, ok := lookupCommandFactoryByName(j.Type)
	if !ok {
		return nil, nil, fmt.Errorf("velocity/bus: no factory registered for command type %q (did you call bus.Register on the worker side?)", j.Type)
	}

	cmd := factory()
	if cmd == nil {
		return nil, nil, fmt.Errorf("velocity/bus: command factory for %q returned nil", j.Type)
	}

	// json.Unmarshal needs an addressable target. The factory returns a
	// zero value of the command type (typically a struct), so we take its
	// address, unmarshal into it, then deref back to the value form the
	// producer dispatched.
	target := reflect.New(reflect.TypeOf(cmd))
	target.Elem().Set(reflect.ValueOf(cmd))
	if len(j.Data) > 0 {
		if err := json.Unmarshal(j.Data, target.Interface()); err != nil {
			return nil, nil, fmt.Errorf("velocity/bus: failed to unmarshal command payload into %q: %w", j.Type, err)
		}
	}
	cmd = target.Elem().Interface()

	b := getDefaultBus()
	if b == nil {
		return nil, nil, fmt.Errorf("velocity/bus: no default bus installed; call bus.SetDefaultBus on the worker side before processing queued commands")
	}
	return cmd, b, nil
}

func (j *commandJob) Failed(err error) {
	b := j.bus
	if b == nil {
		b = getDefaultBus()
	}
	if b == nil {
		return
	}

	b.mu.RLock()
	dispatchEvent := b.dispatchEvent
	b.mu.RUnlock()

	if dispatchEvent != nil {
		cmdType := j.Type
		if cmdType == "" && j.cmd != nil {
			cmdType = reflect.TypeOf(j.cmd).String()
		}
		// Event dispatch errors are intentionally ignored, event dispatch is
		// best-effort and must not interfere with queue worker error handling.
		_ = dispatchEvent(&CommandFailed{CommandType: cmdType, Error: err.Error()})
	}
}

// --- package-level command factory + default-bus registries ---------------

var (
	commandFactoryMu sync.RWMutex
	// commandFactories maps reflect.TypeOf(cmd).String() to a factory that
	// returns a fresh zero value of the command type. The factory shape
	// returns Command (any) rather than a typed value so the registry can
	// hold heterogeneous command kinds; resolveCommand uses reflect to
	// build an addressable target for json.Unmarshal.
	commandFactories = make(map[string]func() Command)
	// commandTypes mirrors commandFactories but keyed on reflect.Type so
	// the producer side (DispatchAsync) can answer "is there a factory
	// for this command?" without paying the reflect.Type -> string cost
	// on every dispatch.
	commandTypes = make(map[reflect.Type]struct{})

	defaultBusMu sync.RWMutex
	defaultBus   *Bus
)

func registerCommandFactory(cmdType reflect.Type, factory func() Command) {
	if cmdType == nil || factory == nil {
		return
	}
	commandFactoryMu.Lock()
	commandFactories[cmdType.String()] = factory
	commandTypes[cmdType] = struct{}{}
	commandFactoryMu.Unlock()
}

func lookupCommandFactory(cmdType reflect.Type) (func() Command, bool) {
	if cmdType == nil {
		return nil, false
	}
	commandFactoryMu.RLock()
	_, ok := commandTypes[cmdType]
	commandFactoryMu.RUnlock()
	if !ok {
		return nil, false
	}
	return lookupCommandFactoryByName(cmdType.String())
}

func lookupCommandFactoryByName(name string) (func() Command, bool) {
	commandFactoryMu.RLock()
	factory, ok := commandFactories[name]
	commandFactoryMu.RUnlock()
	return factory, ok
}

// SetDefaultBus installs the bus that cross-process queue workers dispatch
// hydrated commands through. New() calls this automatically with the bus it
// returns; apps that construct multiple buses can call SetDefaultBus to pin
// the one workers should use.
func SetDefaultBus(b *Bus) {
	defaultBusMu.Lock()
	defaultBus = b
	defaultBusMu.Unlock()
}

func getDefaultBus() *Bus {
	defaultBusMu.RLock()
	defer defaultBusMu.RUnlock()
	return defaultBus
}

// commandJobFactory is registered with queue.RegisterJob in init() so durable
// queue drivers can rehydrate a *commandJob from persisted JSON bytes. The
// hydrated job has cmd / bus / cmdType == nil and Handle reconstructs the
// command via the package-level factory registry plus the default bus.
func commandJobFactory(data []byte) (*commandJob, error) {
	job := &commandJob{}
	if err := json.Unmarshal(data, job); err != nil {
		return nil, fmt.Errorf("velocity/bus: failed to unmarshal commandJob payload: %w", err)
	}
	return job, nil
}

func init() {
	queue.RegisterJob(commandJobFactory)
}
