package queue

import (
	"context"

	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// RunFailedHook runs job's Failed hook for err under ctx, the context the
// driver received with the failure, and contains a panic in it: a job
// implementing FailedCtxer gets FailedCtx(ctx, err), any other job
// Failed(err). A nil ctx is context.Background(). A driver calls it once
// the failure is recorded: the hook is application code, and a panic there
// must not unwind the worker, which would end the worker loop and skip the
// job's terminal bookkeeping (batch counters, the queue.job.failed event). A
// panic comes back as ErrFailedHookPanicked wrapping the recovered value (a
// *panicerr.Error), so the worker can log it and still treat the failure as
// recorded; nil means the hook returned normally.
func RunFailedHook(ctx context.Context, job Job, err error) (hookErr error) {
	defer func() {
		if r := recover(); r != nil {
			hookErr = errchain.Errorf("%w: %w", ErrFailedHookPanicked, panicerr.FromRecovered(r))
		}
	}()
	if ctx == nil {
		ctx = context.Background()
	}
	if fc, ok := job.(FailedCtxer); ok {
		fc.FailedCtx(ctx, err)
		return nil
	}
	job.Failed(err)
	return nil
}
