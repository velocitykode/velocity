package queue

import (
	"fmt"

	"github.com/velocitykode/velocity/internal/panicerr"
)

// RunFailedHook runs job's Failed hook for err and contains a panic in it.
// A driver calls it once the failure is recorded: the hook is application
// code, and a panic there must not unwind the worker, which would end the
// worker loop and skip the job's terminal bookkeeping (batch counters, the
// job.failed event). A panic comes back as ErrFailedHookPanicked wrapping
// the recovered value (a *panicerr.Error), so the worker can log it and
// still treat the failure as recorded; nil means the hook returned
// normally.
func RunFailedHook(job Job, err error) (hookErr error) {
	defer func() {
		if r := recover(); r != nil {
			hookErr = fmt.Errorf("%w: %w", ErrFailedHookPanicked, panicerr.FromRecovered(r))
		}
	}()
	job.Failed(err)
	return nil
}
