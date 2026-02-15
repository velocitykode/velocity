package pipeline

// Stage is a pipeline stage that processes a passable value.
// Implement this interface on structs to create reusable, class-style pipes.
type Stage[T any] interface {
	Handle(passable T, next func(T) error) error
}

// Pipe adapts a function to the Stage interface.
type Pipe[T any] func(passable T, next func(T) error) error

// Handle implements Stage.
func (p Pipe[T]) Handle(passable T, next func(T) error) error {
	return p(passable, next)
}

// Pipeline sends a passable through a series of stages.
type Pipeline[T any] struct {
	passable T
	pipes    []Stage[T]
}

// New creates a new Pipeline.
func New[T any]() *Pipeline[T] {
	return &Pipeline[T]{}
}

// Send sets the object being sent through the pipeline.
func (p *Pipeline[T]) Send(passable T) *Pipeline[T] {
	p.passable = passable
	return p
}

// Through sets the array of stages.
func (p *Pipeline[T]) Through(stages ...Stage[T]) *Pipeline[T] {
	p.pipes = stages
	return p
}

// Pipe appends additional stages.
func (p *Pipeline[T]) Pipe(stages ...Stage[T]) *Pipeline[T] {
	p.pipes = append(p.pipes, stages...)
	return p
}

// Then runs the pipeline with a final destination handler.
func (p *Pipeline[T]) Then(destination func(T) error) error {
	h := destination
	for i := len(p.pipes) - 1; i >= 0; i-- {
		current := p.pipes[i]
		nextH := h
		h = func(passable T) error {
			return current.Handle(passable, nextH)
		}
	}
	return h(p.passable)
}

// ThenReturn runs the pipeline with a no-op destination.
func (p *Pipeline[T]) ThenReturn() error {
	return p.Then(func(_ T) error { return nil })
}
