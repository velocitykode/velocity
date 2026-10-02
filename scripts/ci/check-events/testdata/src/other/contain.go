package other

// Outside the events and orm trees: the contain rule does not apply.
type Listener interface {
	Handle(event any) error
}

type Registry struct {
	listeners []Listener
}

func (r *Registry) Dispatch(event any) error {
	return r.listeners[0].Handle(event)
}
