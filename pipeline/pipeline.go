package pipeline

// Stage is a pipeline stage that processes a passable value.
// Implement this interface on structs to create reusable, class-style pipes.
//
// Pipeline is not safe for concurrent use. Build or execute pipelines from
// a single goroutine, or protect access externally.
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

// Through sets the stages, replacing any previously set.
func (p *Pipeline[T]) Through(stages ...Stage[T]) *Pipeline[T] {
	p.pipes = stages
	return p
}

// Add appends additional stages to the existing set.
func (p *Pipeline[T]) Add(stages ...Stage[T]) *Pipeline[T] {
	p.pipes = append(p.pipes, stages...)
	return p
}

// Build compiles the pipeline into a single callable without executing it.
// Use this to pre-compile a pipeline once and invoke it many times,
// avoiding per-call chain construction overhead.
// The passable set via Send is not used; the returned function accepts
// its own argument.
func (p *Pipeline[T]) Build(destination func(T) error) func(T) error {
	h := destination
	for i := len(p.pipes) - 1; i >= 0; i-- {
		current := p.pipes[i]
		nextH := h
		h = func(passable T) error {
			return current.Handle(passable, nextH)
		}
	}
	return h
}

// Then runs the pipeline with a final destination handler.
func (p *Pipeline[T]) Then(destination func(T) error) error {
	return p.Build(destination)(p.passable)
}

// ThenReturn runs the pipeline with a no-op destination.
func (p *Pipeline[T]) ThenReturn() error {
	return p.Then(func(_ T) error { return nil })
}
