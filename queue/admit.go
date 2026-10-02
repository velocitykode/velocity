package queue

import (
	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/nilval"
)

// Admission: every entry point that takes a job from its caller (a push of
// any driver, a fake's push, a batch dispatch) admits the job here before
// anything else touches it. A job's optional interfaces (OnQueue,
// SetBatchID, JobID, MaxAttempts, a MarshalJSON) are user methods: on a
// typed nil pointer they dereference nil and panic, and a refusal that
// came after one of them would have run user code, logged or stored state
// for a job that is then refused. Admitting first makes the refusal the
// first thing that happens, so a refused push leaves no trace: nothing
// stored, nothing logged, no warning consumed, no panic.
//
// scripts/ci/check-events (rule "admission") holds every exported push of
// the module to it: the first statement that touches the job calls the
// admission, or hands the job unchanged to another push that does.

// admitJob refuses a nil job, untyped or a typed nil, with [ErrNilJob],
// then returns the queue the job goes to: the caller's explicit name when
// non-empty, else the job's OnQueue when it implements [OnQueuer] and
// returns a non-empty name, else "default". OnQueue runs only on an
// admitted job.
func admitJob(job Job, queueName ...string) (string, error) {
	if nilval.Is(job) {
		return "", ErrNilJob
	}
	if len(queueName) > 0 && queueName[0] != "" {
		return queueName[0], nil
	}
	if oq, ok := job.(OnQueuer); ok {
		if name := oq.OnQueue(); name != "" {
			return name, nil
		}
	}
	return "default", nil
}

// admitBatch admits every job of a batch before the batch exists: a nil
// job (ErrNilJob) or one that does not implement Batchable
// (ErrJobNotBatchable, since the worker settles a batch only through its
// jobs' batch ids) refuses the batch whole, before it is saved and before
// its first push, so a refused batch has no row, no registered callbacks
// and no pushed job that would wait on it. The error names the first
// refused job by its position.
func admitBatch(jobs []Job) error {
	for i, job := range jobs {
		if nilval.Is(job) {
			return errchain.Errorf("batch: job %d/%d: %w", i+1, len(jobs), ErrNilJob)
		}
		if _, ok := job.(Batchable); !ok {
			return errchain.Errorf("batch: job %d/%d: %w", i+1, len(jobs), ErrJobNotBatchable)
		}
	}
	return nil
}
