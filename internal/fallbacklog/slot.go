package fallbacklog

import (
	"sync"

	"github.com/velocitykode/velocity/contract"
)

// Slot holds a package-level logger that one goroutine may replace while
// others write through it (the async and trace packages' loggers). Set
// returns only after every write in flight through the logger it replaced
// has finished, so the caller may close that logger once Set returns and
// no line reaches it afterwards. The zero value holds the fallback logger.
type Slot struct {
	mu  sync.RWMutex
	cur *slotEntry
}

// slotEntry is one installed logger and the writes in flight through it.
type slotEntry struct {
	logger   contract.Logger
	inflight sync.WaitGroup
}

// Set installs l (nil installs the fallback logger) and waits for every
// write Use started through the logger it replaces. A write started after
// Set installed l goes to l. It must not be called from inside a Use
// callback on the same Slot: it would wait for itself.
func (s *Slot) Set(l contract.Logger) {
	next := &slotEntry{logger: Resolve(l)}
	s.mu.Lock()
	prev := s.cur
	s.cur = next
	s.mu.Unlock()
	if prev != nil {
		// Every Add on prev happened under the read lock before the swap
		// above, so none can follow this Wait.
		prev.inflight.Wait()
	}
}

// Get returns the installed logger. The caller's writes through it are
// not tracked: a Set may return, and the logger be closed, while they
// run. Framework code writes through Use.
func (s *Slot) Get() contract.Logger {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cur == nil {
		return Logger{}
	}
	return s.cur.logger
}

// Use calls fn with the installed logger and counts the call as a write in
// flight through it until fn returns, so a Set replacing that logger waits
// for fn.
func (s *Slot) Use(fn func(contract.Logger)) {
	s.mu.RLock()
	e := s.cur
	if e != nil {
		e.inflight.Add(1)
	}
	s.mu.RUnlock()
	if e == nil {
		fn(Logger{})
		return
	}
	defer e.inflight.Done()
	fn(e.logger)
}
