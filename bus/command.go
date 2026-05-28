package bus

// Command is any value that represents a command to be dispatched.
type Command = interface{}

// SelfHandling commands can handle themselves without a registered handler.
type SelfHandling interface {
	Handle() error
}

// Handler is a typed function that handles a specific command type.
type Handler[T any] func(cmd T) error

// QueuePusher is satisfied by queue.Driver via structural typing.
// This avoids importing the queue package directly.
type QueuePusher interface {
	Push(job interface {
		Handle() error
		Failed(error)
	}, queue ...string) error
}
