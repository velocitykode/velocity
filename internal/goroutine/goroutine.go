// Package goroutine identifies the running goroutine, for the framework's
// re-entry guards: a hook that must not be handed a failure it caused, a
// drain that must refuse to wait on the goroutine asking for it. Those
// guards cannot be built from a context, which a re-entrant call need not
// carry. It imports only the standard library.
package goroutine

import (
	"runtime"
	"sync"
	"sync/atomic"
)

// parseFallback feeds ID's failure path with unique sentinels. Sentinels
// live above 1<<63 so they can never collide with a real goroutine ID
// within the lifetime of a process.
var parseFallback atomic.Uint64

// ID returns the running goroutine's ID by parsing the first line of
// runtime.Stack ("goroutine N [...]"). It costs about a microsecond, more
// on a deep stack, so the guards call it on failure, drain and
// once-per-run paths only.
//
// The header format is not a formally stable runtime API (though it has
// been stable in practice for many releases and is relied on by widely
// used libraries), so the failure mode is chosen deliberately: if parsing
// ever fails, ID returns a process-unique sentinel instead of a shared
// zero value. A shared zero would make every unparsed goroutine look like
// the same goroutine and falsely trip unrelated guards; a unique sentinel
// merely degrades the guard to a no-op for that one call, which errs on
// the side of reporting rather than suppressing.
func ID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	const prefix = "goroutine "
	s := buf[:n]
	if len(s) <= len(prefix) {
		return 1<<63 | parseFallback.Add(1)
	}
	var id uint64
	digits := 0
	for _, c := range s[len(prefix):] {
		if c < '0' || c > '9' {
			break
		}
		id = id*10 + uint64(c-'0')
		digits++
	}
	if digits == 0 {
		return 1<<63 | parseFallback.Add(1)
	}
	return id
}

// Set is a set of goroutines, each in it between its Enter and its
// matching Leave. A goroutine may enter again while it is in the set; it
// leaves at its last Leave. The zero value is an empty set, ready to use,
// and a Set is safe for concurrent use.
//
// Its methods take the caller's ID rather than taking it themselves: ID's
// cost grows with the depth of the stack it is called from, so a guard
// takes it once, as shallow as it can.
type Set struct {
	mu  sync.Mutex
	ids map[uint64]int
}

// Enter adds goroutine id, the caller's ID(), to s, once more if it is
// in s already. A guard that refuses re-entry checks Contains first.
func (s *Set) Enter(id uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ids == nil {
		s.ids = make(map[uint64]int)
	}
	s.ids[id]++
}

// Leave undoes one Enter by goroutine id; the goroutine leaves s when it
// has left as often as it entered. An id not in s is ignored.
func (s *Set) Leave(id uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch n := s.ids[id]; {
	case n > 1:
		s.ids[id] = n - 1
	case n == 1:
		delete(s.ids, id)
	}
}

// Contains reports whether goroutine id, the caller's ID(), is in s.
func (s *Set) Contains(id uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ids[id] > 0
}
