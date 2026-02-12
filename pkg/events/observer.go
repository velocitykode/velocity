package events

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// ModelObserver interface for observing model lifecycle events
type ModelObserver interface {
	// Creating is called before a model is created
	Creating(model interface{}) error
	// Created is called after a model is created
	Created(model interface{}) error
	// Updating is called before a model is updated
	Updating(model interface{}) error
	// Updated is called after a model is updated
	Updated(model interface{}) error
	// Saving is called before a model is saved (create or update)
	Saving(model interface{}) error
	// Saved is called after a model is saved (create or update)
	Saved(model interface{}) error
	// Deleting is called before a model is deleted
	Deleting(model interface{}) error
	// Deleted is called after a model is deleted
	Deleted(model interface{}) error
	// Restoring is called before a soft-deleted model is restored
	Restoring(model interface{}) error
	// Restored is called after a soft-deleted model is restored
	Restored(model interface{}) error
}

// BaseObserver provides a default implementation of ModelObserver
// Embed this in your observer to only override the methods you need
type BaseObserver struct{}

func (o *BaseObserver) Creating(model interface{}) error  { return nil }
func (o *BaseObserver) Created(model interface{}) error   { return nil }
func (o *BaseObserver) Updating(model interface{}) error  { return nil }
func (o *BaseObserver) Updated(model interface{}) error   { return nil }
func (o *BaseObserver) Saving(model interface{}) error    { return nil }
func (o *BaseObserver) Saved(model interface{}) error     { return nil }
func (o *BaseObserver) Deleting(model interface{}) error  { return nil }
func (o *BaseObserver) Deleted(model interface{}) error   { return nil }
func (o *BaseObserver) Restoring(model interface{}) error { return nil }
func (o *BaseObserver) Restored(model interface{}) error  { return nil }

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
func (r *ObserverRegistry) Fire(event string, model interface{}) error {
	modelType := r.getModelType(model)
	observers := r.GetObservers(modelType)

	for _, observer := range observers {
		if err := r.fireEvent(observer, event, model); err != nil {
			return err
		}
	}

	return nil
}

// fireEvent fires a specific event on an observer
func (r *ObserverRegistry) fireEvent(observer ModelObserver, event string, model interface{}) error {
	switch strings.ToLower(event) {
	case "creating":
		return observer.Creating(model)
	case "created":
		return observer.Created(model)
	case "updating":
		return observer.Updating(model)
	case "updated":
		return observer.Updated(model)
	case "saving":
		return observer.Saving(model)
	case "saved":
		return observer.Saved(model)
	case "deleting":
		return observer.Deleting(model)
	case "deleted":
		return observer.Deleted(model)
	case "restoring":
		return observer.Restoring(model)
	case "restored":
		return observer.Restored(model)
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

// ObservableModel interface for models that can be observed
type ObservableModel interface {
	// GetModelName returns the model type name
	GetModelName() string
	// FireEvent fires an event for this model
	FireEvent(event string) error
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
func (d *ObservableDispatcher) FireModelEvent(event string, model interface{}) error {
	// Fire to model observers
	if err := d.registry.Fire(event, model); err != nil {
		return err
	}

	// Also dispatch as a regular event
	modelType := d.registry.getModelType(model)
	eventName := fmt.Sprintf("%s.%s", strings.ToLower(modelType), event)

	return d.Dispatch(&ModelEvent{
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
func (o *AutoObserver) Creating(model interface{}) error {
	return o.callMethod("Creating", model)
}

// Created calls the Created method if it exists
func (o *AutoObserver) Created(model interface{}) error {
	return o.callMethod("Created", model)
}

// Updating calls the Updating method if it exists
func (o *AutoObserver) Updating(model interface{}) error {
	return o.callMethod("Updating", model)
}

// Updated calls the Updated method if it exists
func (o *AutoObserver) Updated(model interface{}) error {
	return o.callMethod("Updated", model)
}

// Saving calls the Saving method if it exists
func (o *AutoObserver) Saving(model interface{}) error {
	return o.callMethod("Saving", model)
}

// Saved calls the Saved method if it exists
func (o *AutoObserver) Saved(model interface{}) error {
	return o.callMethod("Saved", model)
}

// Deleting calls the Deleting method if it exists
func (o *AutoObserver) Deleting(model interface{}) error {
	return o.callMethod("Deleting", model)
}

// Deleted calls the Deleted method if it exists
func (o *AutoObserver) Deleted(model interface{}) error {
	return o.callMethod("Deleted", model)
}

// Restoring calls the Restoring method if it exists
func (o *AutoObserver) Restoring(model interface{}) error {
	return o.callMethod("Restoring", model)
}

// Restored calls the Restored method if it exists
func (o *AutoObserver) Restored(model interface{}) error {
	return o.callMethod("Restored", model)
}

// callMethod dynamically calls a method on the instance
func (o *AutoObserver) callMethod(methodName string, model interface{}) error {
	instanceValue := reflect.ValueOf(o.instance)
	method := instanceValue.MethodByName(methodName)

	if !method.IsValid() {
		// Method doesn't exist, use base implementation
		return nil
	}

	// Call the method
	results := method.Call([]reflect.Value{reflect.ValueOf(model)})

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
	condition func(event string, model interface{}) bool
}

// NewConditionalObserver creates an observer that only fires when condition is true
func NewConditionalObserver(observer ModelObserver, condition func(string, interface{}) bool) *ConditionalObserver {
	return &ConditionalObserver{
		observer:  observer,
		condition: condition,
	}
}

// Creating conditionally calls the wrapped observer
func (o *ConditionalObserver) Creating(model interface{}) error {
	if o.condition("creating", model) {
		return o.observer.Creating(model)
	}
	return nil
}

// Created conditionally calls the wrapped observer
func (o *ConditionalObserver) Created(model interface{}) error {
	if o.condition("created", model) {
		return o.observer.Created(model)
	}
	return nil
}

// Updating conditionally calls the wrapped observer
func (o *ConditionalObserver) Updating(model interface{}) error {
	if o.condition("updating", model) {
		return o.observer.Updating(model)
	}
	return nil
}

// Updated conditionally calls the wrapped observer
func (o *ConditionalObserver) Updated(model interface{}) error {
	if o.condition("updated", model) {
		return o.observer.Updated(model)
	}
	return nil
}

// Saving conditionally calls the wrapped observer
func (o *ConditionalObserver) Saving(model interface{}) error {
	if o.condition("saving", model) {
		return o.observer.Saving(model)
	}
	return nil
}

// Saved conditionally calls the wrapped observer
func (o *ConditionalObserver) Saved(model interface{}) error {
	if o.condition("saved", model) {
		return o.observer.Saved(model)
	}
	return nil
}

// Deleting conditionally calls the wrapped observer
func (o *ConditionalObserver) Deleting(model interface{}) error {
	if o.condition("deleting", model) {
		return o.observer.Deleting(model)
	}
	return nil
}

// Deleted conditionally calls the wrapped observer
func (o *ConditionalObserver) Deleted(model interface{}) error {
	if o.condition("deleted", model) {
		return o.observer.Deleted(model)
	}
	return nil
}

// Restoring conditionally calls the wrapped observer
func (o *ConditionalObserver) Restoring(model interface{}) error {
	if o.condition("restoring", model) {
		return o.observer.Restoring(model)
	}
	return nil
}

// Restored conditionally calls the wrapped observer
func (o *ConditionalObserver) Restored(model interface{}) error {
	if o.condition("restored", model) {
		return o.observer.Restored(model)
	}
	return nil
}
