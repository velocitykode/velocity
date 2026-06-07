package bus

import (
	"context"

	"github.com/velocitykode/velocity/contract"
)

// Command is any value that represents a command to be dispatched.
type Command = interface{}

// SelfHandling commands can handle themselves without a registered handler.
type SelfHandling interface {
	Handle() error
}

// Handler is a typed function that handles a specific command type.
type Handler[T any] func(cmd T) error

// QueuePusher is the slice of contract.QueueDriver that async dispatch needs.
// It is the real driver's context-aware push method, so any shipped driver
// (queue.MemoryDriver, queue.DatabaseDriver, the Redis driver, ...) and the
// queuetest fakes satisfy it directly. The previous non-Ctx Push(job, queue...)
// shape matched no real driver, leaving SetQueue uncallable and the async path
// dead. commandJob implements contract.QueueJob (Handle/Failed), so the bus's
// own job value is a valid argument.
type QueuePusher interface {
	PushCtx(ctx context.Context, job contract.QueueJob, queue ...string) error
}
