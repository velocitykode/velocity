package fallbacklog

import (
	"sync/atomic"

	"github.com/velocitykode/velocity/contract"
)

// Once writes one line at most once: a one-time warning. The first Write
// claims the line with one atomic swap and then writes it through Write,
// with nothing held; every other Write returns at once, including a
// concurrent one and one the logger makes from inside that line. Unlike
// sync.Once, no caller waits for the line: a warning produces no result a
// caller needs, and a logger that calls back into the component writing
// it must not deadlock. The zero value is ready to use.
type Once struct {
	claimed atomic.Bool
}

// Write writes the line through l, as Write does, when no Write on o has
// claimed it yet. A nil write claims nothing.
func (o *Once) Write(l contract.Logger, write func(contract.Logger), fallbackFields ...any) {
	if write == nil || !o.claimed.CompareAndSwap(false, true) {
		return
	}
	Write(l, write, fallbackFields...)
}
