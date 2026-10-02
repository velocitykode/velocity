package events

import (
	"errors"
	"sync/atomic"
)

// Callback interfaces: held by a registry below, named for a callback kind.
type ModelObserver interface {
	Created(model any) error
}

type Listener interface {
	Handle(event any) error
}

type ShouldHandle interface {
	ShouldHandle(event any) bool
}

type Subscriber interface {
	Subscribe(r *Registry)
}

// ListenerAlias reaches the registry through an alias.
type ListenerAlias = Listener

// Driver is held by a registry but is no callback kind: not checked.
type Driver interface {
	Query() error
}

type entry struct {
	id       int
	listener ListenerAlias
}

type Registry struct {
	observers   map[string][]ModelObserver
	entries     []entry
	subscribers []Subscriber
	drivers     map[string]Driver
	stmt        atomic.Pointer[ModelObserver]
}

func (r *Registry) FireBare(m any) error {
	for _, o := range r.observers["x"] {
		if err := o.Created(m); err != nil { // want contain
			return err
		}
	}
	return nil
}

func (r *Registry) FireStored(m any) error {
	if o := r.stmt.Load(); o != nil {
		return (*o).Created(m) // want contain
	}
	return nil
}

func (r *Registry) FireContained(m any) error {
	for _, o := range r.observers["x"] {
		if err := fireOne(o, m); err != nil {
			return err
		}
	}
	return nil
}

// fireOne defers the recover: contained.
func fireOne(o ModelObserver, m any) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = errors.New("panic")
		}
	}()
	return o.Created(m)
}

// FireViaHelper calls an unexported helper only from a contained function.
func (r *Registry) FireViaHelper(m any) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("panic")
		}
	}()
	return r.fireInner(m)
}

func (r *Registry) fireInner(m any) error {
	return r.observers["x"][0].Created(m)
}

// FireUncontainedHelper reaches an unexported helper from an exported
// function with no recover: the helper's call is not contained.
func (r *Registry) FireUncontainedHelper(m any) error {
	return r.dispatchOne(r.entries[0].listener, m)
}

func (r *Registry) dispatchOne(l Listener, m any) error {
	if h, ok := l.(ShouldHandle); ok && !h.ShouldHandle(m) { // want contain
		return nil
	}
	return l.Handle(m) // want contain
}

// Dispatch passes a closure through a parameter chain to a contained call.
func (r *Registry) Dispatch(event any) error {
	deliver := func(l Listener) error {
		return l.Handle(event)
	}
	var errs []error
	for _, e := range r.entries {
		if err := deliverContained(deliver, e.listener); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func deliverContained(deliver func(Listener) error, l Listener) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = errors.New("panic")
		}
	}()
	return deliver(l)
}

// DispatchMethodValue passes a method value to a contained parameter.
func (r *Registry) DispatchMethodValue(event any) error {
	return runEach(r.entries, event, r.handle)
}

func runEach(entries []entry, event any, fn func(Listener, any) error) error {
	for _, e := range entries {
		l := e.listener
		if err := deliverContained(func(Listener) error { return fn(l, event) }, l); err != nil {
			return err
		}
	}
	return nil
}

func (r *Registry) handle(l Listener, event any) error {
	return l.Handle(event)
}

// DispatchGo starts a goroutine: the recover of the starting function does
// not cover it.
func (r *Registry) DispatchGo(event any) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("panic")
		}
	}()
	for _, e := range r.entries {
		go func() { //safe-goroutine: golden case, a goroutine no recover covers
			_ = e.listener.Handle(event) // want contain
		}()
	}
	return nil
}

// DispatchEscaped stores a closure in a field: not provable.
func (r *Registry) DispatchEscaped(event any) {
	f := func(l Listener) error { return l.Handle(event) } // want contain
	keep = f
}

var keep func(Listener) error

// Subscribe runs the caller's own value on its goroutine: exempt.
func (r *Registry) Subscribe(s Subscriber) {
	r.subscribers = append(r.subscribers, s)
	s.Subscribe(r)
}

// Resubscribe calls registered subscribers: not the caller's own value.
func (r *Registry) Resubscribe() {
	for _, s := range r.subscribers {
		s.Subscribe(r) // want contain
	}
}

// Conditional wraps another observer and is one itself: exempt.
type Conditional struct {
	inner ModelObserver
}

func (c *Conditional) Created(m any) error {
	return c.inner.Created(m)
}

// Query calls a driver: not a callback kind.
func (r *Registry) Query() error {
	return r.drivers["x"].Query()
}
