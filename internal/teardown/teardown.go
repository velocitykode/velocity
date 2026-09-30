// Package teardown runs the steps of a component's shutdown contained: a
// step that panics becomes that step's error, so one bad child (a module,
// a store, a channel, a disk) cannot abort the shutdown of the ones after
// it. The caller runs every step and joins the errors.
//
// It imports the standard library and internal/panicerr only.
package teardown

import "github.com/velocitykode/velocity/internal/panicerr"

// Step runs one teardown step and returns its error, or the panic it
// raised as a *panicerr.Error holding the raw recovered value (formatted
// only when the error is read). A step that calls runtime.Goexit is not a
// panic: Goexit goes on to end the calling goroutine.
func Step(step func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = panicerr.FromRecovered(r)
		}
	}()
	return step()
}
