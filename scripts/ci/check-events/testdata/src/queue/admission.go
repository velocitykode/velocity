// Package queue stands in for the framework's queue package: its
// admission functions and pushes that admit, or fail to admit, a job.
package queue

import (
	"context"
	"errors"

	"example.com/m/contract"
)

type Job = contract.QueueJob

type OnQueuer interface{ OnQueue() string }

type Batchable interface {
	GetBatchID() string
	SetBatchID(id string)
}

var ErrNilJob = errors.New("nil job")

func admitJob(job Job, queueName ...string) (string, error) {
	if job == nil {
		return "", ErrNilJob
	}
	if len(queueName) > 0 {
		return queueName[0], nil
	}
	return "default", nil
}

func AdmitJob(job Job, queueName ...string) (string, error) { return admitJob(job, queueName...) }

func admitBatch(jobs []Job) error {
	for _, j := range jobs {
		if j == nil {
			return ErrNilJob
		}
	}
	return nil
}

func store(Job, string) {}

type Driver struct{ log func(string) }

// PushCtx admits first: fine, ctx checks before it touch no job.
func (d *Driver) PushCtx(ctx context.Context, job Job, queueName ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name, err := admitJob(job, queueName...)
	if err != nil {
		return err
	}
	store(job, name)
	return nil
}

// PushIfNotExistsCtx falls back to PushCtx in an if, then admits: fine.
func (d *Driver) PushIfNotExistsCtx(ctx context.Context, job Job, key string, queueName ...string) error {
	if key == "" {
		return d.PushCtx(ctx, job, queueName...)
	}
	if _, err := admitJob(job, queueName...); err != nil {
		return err
	}
	return nil
}

// PushOther delegates whole: fine.
func (d *Driver) PushOther(ctx context.Context, job Job) error {
	return d.PushCtx(ctx, job)
}

// PushResolvingFirst runs the job's OnQueue before the refusal.
func (d *Driver) PushResolvingFirst(ctx context.Context, job Job) error {
	if oq, ok := job.(OnQueuer); ok { // want admission
		_ = oq.OnQueue()
	}
	_, err := admitJob(job)
	return err
}

// PushLoggingFirst hands the job to user code before the refusal.
func (d *Driver) PushLoggingFirst(ctx context.Context, job Job) error {
	d.log(describe(job)) // want admission
	_, err := admitJob(job)
	return err
}

// PushFallbackThenUnadmitted has a fine fallback, then never admits.
func (d *Driver) PushFallbackThenUnadmitted(ctx context.Context, job Job, key string) error {
	if key == "" {
		return d.PushCtx(ctx, job)
	}
	store(job, key) // want admission
	return nil
}

// PushBadFallback's fallback branch uses the job unadmitted.
func (d *Driver) PushBadFallback(ctx context.Context, job Job, key string) error {
	if key == "" {
		store(job, "default") // want admission
		return nil
	}
	_, err := admitJob(job)
	return err
}

// PushAdmitInLiteral admits only in a func literal that need not run.
func (d *Driver) PushAdmitInLiteral(ctx context.Context, job Job) error {
	check := func() error { _, err := admitJob(job); return err } // want admission
	return check()
}

// PushAdmitOther admits a different value than the job.
func (d *Driver) PushAdmitOther(ctx context.Context, job Job, other Job) error {
	if _, err := admitJob(other); err != nil {
		return err
	}
	store(job, "default") // want admission
	return nil
}

// PushNoJob takes no job: not an entry point.
func (d *Driver) PushNoJob(ctx context.Context, name string) error { return nil }

// pushUnexported is not an entry point.
func (d *Driver) pushUnexported(job Job) { store(job, "") }

// FailedCtx is not a push: the rule leaves it alone.
func (d *Driver) FailedCtx(ctx context.Context, job Job) { store(job, "") }

type PendingBatch struct {
	jobs  []Job
	queue string
}

// Dispatch admits the batch whole before it touches a job: fine; len of
// the slice reads no job.
func (pb *PendingBatch) Dispatch(ctx context.Context, d *Driver) error {
	if len(pb.jobs) == 0 {
		return errors.New("empty")
	}
	if err := admitBatch(pb.jobs); err != nil {
		return err
	}
	for _, j := range pb.jobs {
		if err := d.PushCtx(ctx, j, pb.queue); err != nil {
			return err
		}
	}
	return nil
}

type LateBatch struct{ jobs []Job }

// DispatchStampingFirst stamps jobs before the batch is admitted.
func (lb *LateBatch) DispatchStampingFirst(ctx context.Context, d *Driver) error {
	for _, j := range lb.jobs { // want admission
		if b, ok := j.(Batchable); ok {
			b.SetBatchID("x") // want contain
		}
	}
	return admitBatch(lb.jobs)
}

func describe(j Job) string { _ = j; return "" }
