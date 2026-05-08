package events

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// ModelObserver interface for observing model lifecycle events. Each callback
// receives the caller-supplied ctx so observers see request-scoped values
// (transactions, trace IDs, deadlines) without the model carrying them.
type ModelObserver interface {
	// Creating is called before a model is created
	Creating(ctx context.Context, model interface{}) error
	// Created is called after a model is created
	Created(ctx context.Context, model interface{}) error
	// Updating is called before a model is updated
	Updating(ctx context.Context, model interface{}) error
	// Updated is called after a model is updated
	Updated(ctx context.Context, model interface{}) error
	// Saving is called before a model is saved (create or update)
	Saving(ctx context.Context, model interface{}) error
	// Saved is called after a model is saved (create or update)
	Saved(ctx context.Context, model interface{}) error
	// Deleting is called before a model is deleted
	Deleting(ctx context.Context, model interface{}) error
	// Deleted is called after a model is deleted
	Deleted(ctx context.Context, model interface{}) error
	// Restoring is called before a soft-deleted model is restored
	Restoring(ctx context.Context, model interface{}) error
	// Restored is called after a soft-deleted model is restored
	Restored(ctx context.Context, model interface{}) error
}

// BaseObserver provides a default implementation of ModelObserver
// Embed this in your observer to only override the methods you need
type BaseObserver struct{}

func (o *BaseObserver) Creating(ctx context.Context, model interface{}) error  { return nil }
func (o *BaseObserver) Created(ctx context.Context, model interface{}) error   { return nil }
func (o *BaseObserver) Updating(ctx context.Context, model interface{}) error  { return nil }
func (o *BaseObserver) Updated(ctx context.Context, model interface{}) error   { return nil }
func (o *BaseObserver) Saving(ctx context.Context, model interface{}) error    { return nil }
func (o *BaseObserver) Saved(ctx context.Context, model interface{}) error     { return nil }
func (o *BaseObserver) Deleting(ctx context.Context, model interface{}) error  { return nil }
func (o *BaseObserver) Deleted(ctx context.Context, model interface{}) error   { return nil }
func (o *BaseObserver) Restoring(ctx context.Context, model interface{}) error { return nil }
func (o *BaseObserver) Restored(ctx context.Context, model interface{}) error  { return nil }

// ObserverRegistry manages model observers
type ObserverRegistry struct {
	observers map[string][]ModelObserver // model type -> observers
	mu        sync.RWMutex
}

// NewObserverRegistry creates a new observer registry
func NewObserverRegistry() *ObserverRegistry {
	return &ObserverRegistry{
		observers: make(map[string][]ModelObserver),
	}
}

// Observe registers an observer for a model type
func (r *ObserverRegistry) Observe(modelType string, observer ModelObserver) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.observers[modelType] = append(r.observers[modelType], observer)
}

// ObserveModel registers an observer for a model instance (extracts type)
func (r *ObserverRegistry) ObserveModel(model interface{}, observer ModelObserver) {
	modelType := r.getModelType(model)
	r.Observe(modelType, observer)
}

// GetObservers returns all observers for a model type
func (r *ObserverRegistry) GetObservers(modelType string) []ModelObserver {
	r.mu.RLock()
	defer r.mu.RUnlock()

	observers := r.observers[modelType]
	result := make([]ModelObserver, len(observers))
	copy(result, observers)
	return result
}

// Fire fires a model event to all registered observers
func (r *ObserverRegistry) Fire(ctx context.Context, event string, model interface{}) error {
	if ctx == nil {
		ctx = context.Background()
	}
	modelType := r.getModelType(model)
	observers := r.GetObservers(modelType)

	for _, observer := range observers {
		if err := r.fireEvent(ctx, observer, event, model); err != nil {
			return err
		}
	}

	return nil
}

// fireEvent fires a specific event on an observer
func (r *ObserverRegistry) fireEvent(ctx context.Context, observer ModelObserver, event string, model interface{}) error {
	switch strings.ToLower(event) {
	case "creating":
		return observer.Creating(ctx, model)
	case "created":
		return observer.Created(ctx, model)
	case "updating":
		return observer.Updating(ctx, model)
	case "updated":
		return observer.Updated(ctx, model)
	case "saving":
		return observer.Saving(ctx, model)
	case "saved":
		return observer.Saved(ctx, model)
	case "deleting":
		return observer.Deleting(ctx, model)
	case "deleted":
		return observer.Deleted(ctx, model)
	case "restoring":
		return observer.Restoring(ctx, model)
	case "restored":
		return observer.Restored(ctx, model)
	default:
		return fmt.Errorf("unknown model event: %s", event)
	}
}

// getModelType extracts the type name from a model instance
func (r *ObserverRegistry) getModelType(model interface{}) string {
	t := reflect.TypeOf(model)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t.Name()
}

// ClearObservers removes all observers for a model type
func (r *ObserverRegistry) ClearObservers(modelType string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.observers, modelType)
}

// ClearAll removes all observers
func (r *ObserverRegistry) ClearAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.observers = make(map[string][]ModelObserver)
}

// ObservableModel is the interface for models that can be observed.
// Implementations should plumb the supplied ctx through to observers so
// request/tx-scoped values (deadlines, trace IDs) reach lifecycle hooks.
type ObservableModel interface {
	// GetModelName returns the model type name
	GetModelName() string
	// FireEvent fires an event for this model with the caller-supplied ctx.
	FireEvent(ctx context.Context, event string) error
}

// ObservableDispatcher integrates model observers with the event dispatcher
type ObservableDispatcher struct {
	*SubscriberDispatcher
	registry *ObserverRegistry
}

// NewObservableDispatcher creates a new dispatcher with model observer support
func NewObservableDispatcher() *ObservableDispatcher {
	return &ObservableDispatcher{
		SubscriberDispatcher: NewSubscriberDispatcher(),
		registry:             NewObserverRegistry(),
	}
}

// Observe registers a model observer
func (d *ObservableDispatcher) Observe(modelType string, observer ModelObserver) {
	d.registry.Observe(modelType, observer)
}

// ObserveModel registers an observer for a model instance
func (d *ObservableDispatcher) ObserveModel(model interface{}, observer ModelObserver) {
	d.registry.ObserveModel(model, observer)
}

// FireModelEvent fires a model lifecycle event
func (d *ObservableDispatcher) FireModelEvent(ctx context.Context, event string, model interface{}) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// Fire to model observers
	if err := d.registry.Fire(ctx, event, model); err != nil {
		return err
	}

	// Also dispatch as a regular event
	modelType := d.registry.getModelType(model)
	eventName := fmt.Sprintf("%s.%s", strings.ToLower(modelType), event)

	return d.Dispatch(ctx, &ModelEvent{
		BaseEvent: BaseEvent{EventName: eventName},
		Model:     model,
		Action:    event,
		ModelType: modelType,
	})
}

// ModelEvent represents a model lifecycle event
type ModelEvent struct {
	BaseEvent
	Model     interface{}
	Action    string // creating, created, updating, etc.
	ModelType string
}

// AutoObserver automatically calls model observers based on method names
type AutoObserver struct {
	BaseObserver
	instance interface{}
}

// NewAutoObserver creates an observer that auto-maps methods to model events
func NewAutoObserver(instance interface{}) *AutoObserver {
	return &AutoObserver{
		instance: instance,
	}
}

// Creating calls the Creating method if it exists
func (o *AutoObserver) Creating(ctx context.Context, model interface{}) error {
	return o.callMethod(ctx, "Creating", model)
}

// Created calls the Created method if it exists
func (o *AutoObserver) Created(ctx context.Context, model interface{}) error {
	return o.callMethod(ctx, "Created", model)
}

// Updating calls the Updating method if it exists
func (o *AutoObserver) Updating(ctx context.Context, model interface{}) error {
	return o.callMethod(ctx, "Updating", model)
}

// Updated calls the Updated method if it exists
func (o *AutoObserver) Updated(ctx context.Context, model interface{}) error {
	return o.callMethod(ctx, "Updated", model)
}

// Saving calls the Saving method if it exists
func (o *AutoObserver) Saving(ctx context.Context, model interface{}) error {
	return o.callMethod(ctx, "Saving", model)
}

// Saved calls the Saved method if it exists
func (o *AutoObserver) Saved(ctx context.Context, model interface{}) error {
	return o.callMethod(ctx, "Saved", model)
}

// Deleting calls the Deleting method if it exists
func (o *AutoObserver) Deleting(ctx context.Context, model interface{}) error {
	return o.callMethod(ctx, "Deleting", model)
}

// Deleted calls the Deleted method if it exists
func (o *AutoObserver) Deleted(ctx context.Context, model interface{}) error {
	return o.callMethod(ctx, "Deleted", model)
}

// Restoring calls the Restoring method if it exists
func (o *AutoObserver) Restoring(ctx context.Context, model interface{}) error {
	return o.callMethod(ctx, "Restoring", model)
}

// Restored calls the Restored method if it exists
func (o *AutoObserver) Restored(ctx context.Context, model interface{}) error {
	return o.callMethod(ctx, "Restored", model)
}

// callMethod dynamically calls a method on the instance. The method may
// optionally accept (ctx, model) as its arguments; methods that only accept
// (model) are still supported for legacy implementations.
func (o *AutoObserver) callMethod(ctx context.Context, methodName string, model interface{}) error {
	instanceValue := reflect.ValueOf(o.instance)
	method := instanceValue.MethodByName(methodName)

	if !method.IsValid() {
		// Method doesn't exist, use base implementation
		return nil
	}

	mt := method.Type()
	var args []reflect.Value
	switch mt.NumIn() {
	case 1:
		args = []reflect.Value{reflect.ValueOf(model)}
	case 2:
		args = []reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(model)}
	default:
		return fmt.Errorf("velocity/events: AutoObserver method %s expected 1 or 2 args, got %d", methodName, mt.NumIn())
	}

	// Call the method
	results := method.Call(args)

	// Check for error return
	if len(results) > 0 && !results[0].IsNil() {
		return results[0].Interface().(error)
	}

	return nil
}

// ConditionalObserver only fires events when conditions are met
type ConditionalObserver struct {
	BaseObserver
	observer  ModelObserver
	condition func(ctx context.Context, event string, model interface{}) bool
}

// NewConditionalObserver creates an observer that only fires when condition is true
func NewConditionalObserver(observer ModelObserver, condition func(context.Context, string, interface{}) bool) *ConditionalObserver {
	return &ConditionalObserver{
		observer:  observer,
		condition: condition,
	}
}

// Creating conditionally calls the wrapped observer
func (o *ConditionalObserver) Creating(ctx context.Context, model interface{}) error {
	if o.condition(ctx, "creating", model) {
		return o.observer.Creating(ctx, model)
	}
	return nil
}

// Created conditionally calls the wrapped observer
func (o *ConditionalObserver) Created(ctx context.Context, model interface{}) error {
	if o.condition(ctx, "created", model) {
		return o.observer.Created(ctx, model)
	}
	return nil
}

// Updating conditionally calls the wrapped observer
func (o *ConditionalObserver) Updating(ctx context.Context, model interface{}) error {
	if o.condition(ctx, "updating", model) {
		return o.observer.Updating(ctx, model)
	}
	return nil
}

// Updated conditionally calls the wrapped observer
func (o *ConditionalObserver) Updated(ctx context.Context, model interface{}) error {
	if o.condition(ctx, "updated", model) {
		return o.observer.Updated(ctx, model)
	}
	return nil
}

// Saving conditionally calls the wrapped observer
func (o *ConditionalObserver) Saving(ctx context.Context, model interface{}) error {
	if o.condition(ctx, "saving", model) {
		return o.observer.Saving(ctx, model)
	}
	return nil
}

// Saved conditionally calls the wrapped observer
func (o *ConditionalObserver) Saved(ctx context.Context, model interface{}) error {
	if o.condition(ctx, "saved", model) {
		return o.observer.Saved(ctx, model)
	}
	return nil
}

// Deleting conditionally calls the wrapped observer
func (o *ConditionalObserver) Deleting(ctx context.Context, model interface{}) error {
	if o.condition(ctx, "deleting", model) {
		return o.observer.Deleting(ctx, model)
	}
	return nil
}

// Deleted conditionally calls the wrapped observer
func (o *ConditionalObserver) Deleted(ctx context.Context, model interface{}) error {
	if o.condition(ctx, "deleted", model) {
		return o.observer.Deleted(ctx, model)
	}
	return nil
}

// Restoring conditionally calls the wrapped observer
func (o *ConditionalObserver) Restoring(ctx context.Context, model interface{}) error {
	if o.condition(ctx, "restoring", model) {
		return o.observer.Restoring(ctx, model)
	}
	return nil
}

// Restored conditionally calls the wrapped observer
func (o *ConditionalObserver) Restored(ctx context.Context, model interface{}) error {
	if o.condition(ctx, "restored", model) {
		return o.observer.Restored(ctx, model)
	}
	return nil
}
