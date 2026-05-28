package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"

	"github.com/google/uuid"
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
//
// The Ctx-suffixed methods are the primary API: they thread the caller's
// context through to the underlying queue driver so a producer-side
// cancellation aborts the enqueue round-trip instead of blocking. The
// non-Ctx variants are retained as `// Deprecated:` shims that pass
// context.Background() so callers compiled against earlier sweep-1
// commits keep working.
type Dispatcher interface {
	Dispatch(cmd Command) error
	DispatchAsyncCtx(ctx context.Context, cmd Command) error
	// Deprecated: use DispatchAsyncCtx with a request-scoped context.Context.
	DispatchAsync(cmd Command) error
}

// Bus dispatches commands to their registered handlers through optional
// middleware.
//
// Every Bus has a stable id and owns its own command factory registry. The
// id is included on every queued commandJob payload so the consumer-side
// hydration path looks up the originating bus, then resolves the command
// type through that bus's factories. This prevents cross-bus contamination
// in setups with multiple Bus instances (multi-tenant apps, plugin systems,
// test harnesses that build a fresh bus per case).
//
// New() assigns a random UUID, which is sufficient for in-process workers
// (memory driver, or same-process consumers of a durable driver). Apps
// that run workers in a separate process from the producer must pin a
// stable id with NewWithID so producer and consumer agree.
type Bus struct {
	id            string
	handlers      map[reflect.Type]any      // type -> handler wrapper func(Command) error
	factories     map[string]func() Command // typename -> command factory for async hydration
	factoryTypes  map[reflect.Type]struct{} // mirrors factories keyed on reflect.Type for cheap lookup
	middleware    []pipeline.Stage[Command]
	queue         QueuePusher
	queueName     string
	dispatchEvent func(event any) error
	mu            sync.RWMutex
}

// New creates a new Bus instance with a randomly generated id. The bus is
// registered in the package-level bus registry under that id so cross-
// process queue workers in the same process can hydrate jobs back through
// it. The id is opaque, never persisted by the framework, and changes
// across process restarts.
//
// Workers running in a different process from the producer must use
// NewWithID with a deterministic id so producer-side and consumer-side
// buses agree on identity.
func New() *Bus {
	return NewWithID(uuid.NewString())
}

// NewWithID creates a new Bus instance with a caller-supplied stable id.
// Use this when the worker process is separate from the producer process
// and the two need to coordinate by name: both call
// NewWithID("orders-bus") and the queued payload's BusID lookup resolves
// on the worker side.
//
// Panics with *contract.RegistrationError if id is empty or if another
// bus is already registered under the same id.
func NewWithID(id string) *Bus {
	if id == "" {
		panic(contract.NewRegistrationError("bus", "bus id must be non-empty"))
	}
	b := &Bus{
		id:           id,
		handlers:     make(map[reflect.Type]any),
		factories:    make(map[string]func() Command),
		factoryTypes: make(map[reflect.Type]struct{}),
	}
	registerBus(b)
	return b
}

// ID returns the bus's stable identifier. The id is written to every
// queued commandJob payload so the consumer side can route hydration
// through the correct bus.
func (b *Bus) ID() string { return b.id }

// Close removes the bus from the package-level bus registry. After Close,
// any in-flight queued commandJob whose BusID matches this bus will fail
// to hydrate with a "bus not registered" error rather than dispatching
// through a partially-shut-down bus. Calling Close is optional, the
// registry entry otherwise lives for the lifetime of the process.
func (b *Bus) Close() {
	unregisterBus(b.id)
}

// Register registers a typed handler for a command type.
// This is a package-level function due to Go generics limitations on methods.
// Panics with *contract.RegistrationError if handler is nil, a handler for the
// same command type is already registered, or the command type cannot be
// JSON-marshalled (required for async dispatch over queues).
//
// Register installs a per-bus factory keyed on the command's type name so
// cross-process queue workers can rehydrate the command from the serialized
// commandJob payload. Registration is scoped to the receiving bus only;
// two buses can hold disjoint handler sets and a command dispatched via
// bus A never falls through to bus B's handler.
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

	// Install the factory in this bus's own registry. The key is the same
	// string reflect.TypeOf(cmd) produces on the producer side, so producer
	// and consumer sides agree by construction.
	b.factories[cmdType.String()] = func() Command {
		var v T
		return v
	}
	b.factoryTypes[cmdType] = struct{}{}
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

// DispatchAsyncCtx wraps the command as a job and pushes it to the queue,
// threading ctx through the queue driver's PushCtx so a producer-side
// cancellation aborts the enqueue round-trip cleanly.
//
// The command must be JSON-marshallable and must have been registered via
// Register[T] on THIS bus. DispatchAsyncCtx refuses to enqueue otherwise so
// the silent-drop hole on durable drivers (Redis, database) is closed at
// the producer side. The wire payload carries the bus's id so the
// consumer-side hydration path routes through the same bus, never a
// different one that happens to hold a handler for the type.
func (b *Bus) DispatchAsyncCtx(ctx context.Context, cmd Command) error {
	if ctx == nil {
		ctx = context.Background()
	}
	b.mu.RLock()
	q, queueName, dispatchEvent := b.queue, b.queueName, b.dispatchEvent
	hasFactory := false
	cmdType := reflect.TypeOf(cmd)
	if cmdType != nil {
		_, hasFactory = b.factoryTypes[cmdType]
	}
	b.mu.RUnlock()

	if q == nil {
		return fmt.Errorf("bus: queue not configured for async dispatch")
	}

	if cmdType == nil {
		return fmt.Errorf("bus: cannot dispatch nil command")
	}

	// Refuse to enqueue when this bus has no factory for the command. The
	// consumer side would otherwise hydrate the payload, look up the bus
	// by id, fail to find a factory in THIS bus's registry, and route the
	// job to failed_jobs. We surface the configuration error here so a
	// missing Register call is caught synchronously.
	if !hasFactory {
		return fmt.Errorf("velocity/bus: refusing to async-dispatch command %s: no factory registered on bus %s (call bus.Register before DispatchAsyncCtx)", cmdType.String(), b.id)
	}

	data, err := json.Marshal(cmd)
	if err != nil {
		return fmt.Errorf("velocity/bus: failed to marshal command %s: %w", cmdType.String(), err)
	}

	job := &commandJob{
		BusID:   b.id,
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

	if err := q.PushCtx(ctx, job, args...); err != nil {
		return fmt.Errorf("bus: failed to push command to queue: %w", err)
	}

	if dispatchEvent != nil {
		_ = dispatchEvent(&CommandQueued{CommandType: cmdType.String()})
	}

	return nil
}

// DispatchAsync wraps the command as a job and pushes it to the queue
// with a background context.
//
// Deprecated: use DispatchAsyncCtx with a request-scoped context.Context
// so producer-side cancellation aborts the enqueue round-trip instead of
// blocking the caller until the queue round-trip times out internally.
func (b *Bus) DispatchAsync(cmd Command) error {
	return b.DispatchAsyncCtx(context.Background(), cmd)
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

// lookupFactory returns the factory registered on this bus for a command
// type name. The bool is false when no factory is registered.
func (b *Bus) lookupFactory(typeName string) (func() Command, bool) {
	b.mu.RLock()
	factory, ok := b.factories[typeName]
	b.mu.RUnlock()
	return factory, ok
}

// commandJob wraps a command as a queue job for async dispatch.
//
// Wire format:
//   - BusID identifies the producing bus. The consumer-side hydration
//     path uses it to find the same bus instance in the package-level
//     bus registry; a missing entry is a real error and surfaces to the
//     worker. This is the cross-bus contamination guard: a job enqueued
//     by bus A cannot resolve through bus B even if B happens to hold a
//     factory for the same command type.
//   - Type is the reflect.TypeOf(cmd).String() identifier registered
//     via Register[T] on the producing bus.
//   - Data is the JSON-encoded command bytes.
//
// All three round-trip through encoding/json so durable drivers (Redis,
// database) can persist the payload and rehydrate the concrete command
// on any worker, in any process that shares the producer's bus ids.
//
// The unexported cmd/bus/cmdType fields are an in-process fast path. The
// memory queue driver retains the live pointer across a same-process Pop
// so the worker can dispatch without consulting any registry. Cross-
// process pops always have cmd == nil after json.Unmarshal and Handle
// reconstructs the value from BusID + Type + Data via the registries.
type commandJob struct {
	BusID string          `json:"bus_id"`
	Type  string          `json:"type"`
	Data  json.RawMessage `json:"data"`

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
// BusID + Type + Data via the package registries and dispatches it
// through the bus identified by BusID.
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
// otherwise the command is rebuilt from BusID + Type + Data via the
// per-bus factory registry on the bus matching BusID.
//
// Failures are explicit errors, never silent fallbacks: a missing BusID,
// an unregistered bus id, or a missing factory on the resolved bus all
// surface to the worker so the job can be routed to failed_jobs / events.
func (j *commandJob) resolveCommand() (Command, *Bus, error) {
	if j.cmd != nil && j.bus != nil {
		return j.cmd, j.bus, nil
	}

	if j.BusID == "" {
		return nil, nil, fmt.Errorf("velocity/bus: commandJob payload missing bus_id (legacy or corrupted payload)")
	}
	if j.Type == "" {
		return nil, nil, fmt.Errorf("velocity/bus: commandJob payload missing type")
	}

	b, ok := lookupBus(j.BusID)
	if !ok {
		return nil, nil, fmt.Errorf("velocity/bus: no bus registered for id %q (did the worker process construct the bus with the same id as the producer? use bus.NewWithID for cross-process setups)", j.BusID)
	}

	factory, ok := b.lookupFactory(j.Type)
	if !ok {
		return nil, nil, fmt.Errorf("velocity/bus: no factory registered for command type %q on bus %q (did you call bus.Register on the worker side?)", j.Type, j.BusID)
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

	return cmd, b, nil
}

func (j *commandJob) Failed(err error) {
	b := j.bus
	if b == nil && j.BusID != "" {
		if found, ok := lookupBus(j.BusID); ok {
			b = found
		}
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

// --- package-level bus registry -----------------------------------------

var (
	busRegistryMu sync.RWMutex
	// busRegistry maps a bus's stable id to the live *Bus instance.
	// Each Bus self-registers on construction (New / NewWithID) and
	// unregisters via Close. Cross-process queue workers reconstruct
	// the bus binding by looking up the id encoded on every commandJob
	// payload, so a job enqueued by bus A can ONLY hydrate through bus A.
	busRegistry = make(map[string]*Bus)
)

func registerBus(b *Bus) {
	busRegistryMu.Lock()
	defer busRegistryMu.Unlock()
	if existing, ok := busRegistry[b.id]; ok && existing != b {
		// Two buses claiming the same id is a programming error: the
		// async hydration path can only route to one of them, so the
		// other's jobs would silently route through the wrong handler
		// registry. Surface synchronously instead.
		panic(contract.NewRegistrationError("bus", fmt.Sprintf("bus id %q already registered to a different instance", b.id)))
	}
	busRegistry[b.id] = b
}

func unregisterBus(id string) {
	busRegistryMu.Lock()
	defer busRegistryMu.Unlock()
	delete(busRegistry, id)
}

func lookupBus(id string) (*Bus, bool) {
	busRegistryMu.RLock()
	defer busRegistryMu.RUnlock()
	b, ok := busRegistry[id]
	return b, ok
}

// commandJobFactory is registered with queue.RegisterJob in init() so durable
// queue drivers can rehydrate a *commandJob from persisted JSON bytes. The
// hydrated job has cmd / bus / cmdType == nil and Handle reconstructs the
// command via the bus identified by BusID plus that bus's factory registry.
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
