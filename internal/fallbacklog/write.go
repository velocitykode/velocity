package fallbacklog

import "github.com/velocitykode/velocity/contract"

// Write writes one line through l, or through the fallback Logger when l
// is nil, by calling write with it. It is for lines written where a
// panicking logger must not escape: inside a deferred recover, on a
// goroutine with no recovery, or before cleanup that has to run.
//
// When the write through l panics, write runs once more with the fallback
// Logger, and a panic there is contained too, so Write never panics.
// fallbackFields are the pairs l carried already (bound with With) that
// the line must keep on the fallback; they are bound to the fallback
// Logger only, never added to a line l writes.
func Write(l contract.Logger, write func(contract.Logger), fallbackFields ...any) {
	if write == nil {
		return
	}
	if l != nil && written(l, write) {
		return
	}
	var fb contract.Logger = Logger{}
	if len(fallbackFields) > 0 {
		fb = fb.With(fallbackFields...)
	}
	written(fb, write)
}

// written runs write with l and reports whether it returned without a
// panic.
func written(l contract.Logger, write func(contract.Logger)) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	write(l)
	return true
}
