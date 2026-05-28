package bus

import (
	"context"

	"github.com/velocitykode/velocity/queue"
)

// Command is any value that represents a command to be dispatched.
type Command = interface{}

// SelfHandling commands can handle themselves without a registered handler.
type SelfHandling interface {
	Handle() error
}

// Handler is a typed function that handles a specific command type.
type Handler[T any] func(cmd T) error

// QueuePusher is the interface the bus uses to enqueue jobs for async
// dispatch. It is satisfied by queue.Driver (the framework's standard
// queue plug-in seam), so app.Services.Queue can be passed directly to
// Bus.SetQueue without an adapter.
//
// The signature mirrors queue.Driver.PushCtx exactly. The job is a
// queue.Job, the ctx is the request-scoped context, and queue is the
// optional destination-queue name.
//
// The bus package already imports queue for the consumer-side
// RegisterJob call in init(), so this interface does not introduce new
// coupling. The original structural-typing shape (an anonymous job
// interface) was retained from before queue.Driver carried Ctx-suffixed
// methods; when the Driver was promoted in sweep 1, queue.Driver
// stopped satisfying the structural shape and only an explicit adapter
// (memoryQueueAdapter in queue_roundtrip_test.go) bridged the gap. The
// explicit queue.Driver-shaped signature closes that hole so the
// "satisfied by queue.Driver" claim is checked at compile time.
type QueuePusher interface {
	PushCtx(ctx context.Context, job queue.Job, queue ...string) error
}

// Compile-time guarantee that queue.Driver satisfies QueuePusher. A future
// edit that drifts either signature out of alignment fails the build here
// rather than surfacing as a runtime "missing method" panic when
// app.Services.Queue is wired into Bus.SetQueue.
var _ QueuePusher = (queue.Driver)(nil)
