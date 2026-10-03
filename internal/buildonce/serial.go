package buildonce

import (
	"context"
	"sync"
)

// Serial runs functions one at a time: the transitions of one value that
// each read its state, call user code (a store) and write the state back,
// and must not overlap. Unlike Group, every caller runs its own function;
// nobody joins another caller's result. Like Group, no lock is held while
// the function runs: a small mutex guards the busy flag only, so user code
// in the function can block or panic without holding a lock of the
// component, and a waiter can leave at its context.
//
// The zero value is ready to use. An uncontended Do allocates nothing; the
// channel waiters park on is made when the first waiter arrives.
//
// Every caller waits for the turn, whoever it is: a Serial does not know
// which goroutine holds the turn and does not look. A function that asks
// for its own Serial's turn again (directly, or through user code that
// calls back into the component) therefore waits on itself until its
// context ends, forever when it has none. The component that owns the
// Serial says so where its user code is specified.
type Serial struct {
	mu      sync.Mutex
	busy    bool
	waiting int
	// free is closed when the turn is released while callers wait; nil
	// when nobody waits.
	free chan struct{}
}

// Do takes the turn, runs fn on the calling goroutine and releases the
// turn, and returns nil. While another Do's function runs it waits for the
// turn, or returns ctx.Err() when ctx ends first (fn is then not run). A
// function that panics releases the turn and the panic reaches Do's
// caller.
//
// The order in which waiters get the turn is not promised: when the turn
// is released every waiter is woken and whichever takes the busy flag
// first runs; the others wait again. Each waiter runs exactly once.
func (s *Serial) Do(ctx context.Context, fn func()) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	for s.busy {
		if s.free == nil {
			s.free = make(chan struct{})
		}
		free := s.free
		s.waiting++
		s.mu.Unlock()
		select {
		case <-free:
			s.mu.Lock()
			s.waiting--
		case <-ctx.Done():
			s.mu.Lock()
			s.waiting--
			s.mu.Unlock()
			return ctx.Err()
		}
	}
	s.busy = true
	s.mu.Unlock()

	defer s.release()
	runBuild(fn)
	return nil
}

// release frees the turn and wakes the waiters; one of them takes it.
func (s *Serial) release() {
	s.mu.Lock()
	s.busy = false
	if s.free != nil {
		close(s.free)
		s.free = nil
	}
	s.mu.Unlock()
}

// Waiting returns how many callers are waiting for the turn. A test uses
// it to know a caller is waiting before it lets the turn end.
func (s *Serial) Waiting() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.waiting
}
