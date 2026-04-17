package events

import "errors"

var (
	ErrListenerNotFound  = errors.New("velocity/events: listener not found")
	ErrDispatcherStopped = errors.New("velocity/events: dispatcher stopped")
)
