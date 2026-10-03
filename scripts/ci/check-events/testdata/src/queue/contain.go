package queue

import (
	"context"
	"errors"
)

// The contain rule in the queue tree: Batchable and OnQueuer are callback
// interfaces here with no registry holding them. A call of one of their
// methods on a job the framework holds (a pending batch's, a popped one)
// needs a recover around it; on the caller's own job it does not.

type ContainBatch struct {
	jobs  []Job
	queue string
}

// DispatchBare calls the jobs' methods with no recover: the jobs are the
// receiver's, not a parameter.
func (cb *ContainBatch) DispatchBare(ctx context.Context, d *Driver) error {
	if err := admitBatch(cb.jobs); err != nil {
		return err
	}
	for _, j := range cb.jobs {
		name := cb.queue
		if oq, ok := j.(OnQueuer); ok {
			name = oq.OnQueue() // want contain
		}
		j.(Batchable).SetBatchID("x") // want contain
		if err := d.PushCtx(ctx, j, name); err != nil {
			return err
		}
	}
	return nil
}

// DispatchBound binds each job through the contained helper: fine.
func (cb *ContainBatch) DispatchBound(ctx context.Context, d *Driver) error {
	if err := admitBatch(cb.jobs); err != nil {
		return err
	}
	for _, j := range cb.jobs {
		name, err := bindJob(j, cb.queue)
		if err != nil {
			return err
		}
		if err := d.PushCtx(ctx, j, name); err != nil {
			return err
		}
	}
	return nil
}

// bindJob defers the recover: contained, and so is the helper only it calls.
func bindJob(j Job, fallback string) (name string, err error) {
	defer func() {
		if p := recover(); p != nil {
			name, err = "", errors.New("panic")
		}
	}()
	name = jobQueue(j, fallback)
	j.(Batchable).SetBatchID("x")
	return name, nil
}

func jobQueue(j Job, fallback string) string {
	if oq, ok := j.(OnQueuer); ok {
		if name := oq.OnQueue(); name != "" {
			return name
		}
	}
	return fallback
}

type ContainWorker struct{ d *Driver }

func (d *Driver) pop() Job { return nil }

// processBare reads a popped job's batch id with no recover.
func (w *ContainWorker) processBare() string {
	job := w.d.pop()
	if bj, ok := job.(Batchable); ok {
		return bj.GetBatchID() // want contain
	}
	return ""
}

// processContained reads it through the contained helper: fine.
func (w *ContainWorker) processContained() string {
	id, _ := batchIDOf(w.d.pop())
	return id
}

func batchIDOf(job Job) (id string, err error) {
	bj, ok := job.(Batchable)
	if !ok {
		return "", nil
	}
	defer func() {
		if p := recover(); p != nil {
			id, err = "", errors.New("panic")
		}
	}()
	return bj.GetBatchID(), nil
}

// Run keeps the unexported worker methods reachable.
func (w *ContainWorker) Run() { _ = w.processBare() + w.processContained() }

// PushOwn resolves the caller's own job directly: the caller's call, fine.
func (d *Driver) PushOwn(ctx context.Context, job Job) error {
	if _, err := admitJob(job); err != nil {
		return err
	}
	name := "default"
	if oq, ok := job.(OnQueuer); ok {
		name = oq.OnQueue()
	}
	store(job, name)
	return nil
}

// PushOwnViaHelper hands its own job to an unexported helper, itself only
// ever handed a caller's own job, one more level down included: fine.
func (d *Driver) PushOwnViaHelper(ctx context.Context, job Job) error {
	if _, err := admitJob(job); err != nil {
		return err
	}
	store(job, ownQueue(job))
	return nil
}

func (d *Driver) PushOwnTwoLevels(ctx context.Context, job Job) error {
	if _, err := admitJob(job); err != nil {
		return err
	}
	store(job, ownQueueOuter(job))
	return nil
}

func ownQueueOuter(job Job) string { return ownQueue(job) }

func ownQueue(job Job) string {
	if oq, ok := job.(OnQueuer); ok {
		return oq.OnQueue()
	}
	return "default"
}

// PushMixedHelper's helper is also handed a popped job: its parameter is
// not always the caller's own, so its call needs the recover.
func (d *Driver) PushMixedHelper(ctx context.Context, job Job) error {
	if _, err := admitJob(job); err != nil {
		return err
	}
	store(job, mixedQueue(job))
	return nil
}

func (w *ContainWorker) Requeue() { store(nil, mixedQueue(w.d.pop())) }

func mixedQueue(job Job) string {
	if oq, ok := job.(OnQueuer); ok {
		return oq.OnQueue() // want contain
	}
	return "default"
}

// PushHelperInGo runs its helper on a goroutine of its own: a panic there
// reaches no caller.
func (d *Driver) PushHelperInGo(ctx context.Context, job Job) error {
	if _, err := admitJob(job); err != nil {
		return err
	}
	go goQueue(job) //safe-goroutine: golden case, a helper no caller waits on
	return nil
}

func goQueue(job Job) {
	if oq, ok := job.(OnQueuer); ok {
		_ = oq.OnQueue() // want contain
	}
}

// PushRebound binds one variable to its own job, then to a popped one: the
// variable is not the caller's own value.
func (d *Driver) PushRebound(ctx context.Context, job Job) error {
	if _, err := admitJob(job); err != nil {
		return err
	}
	oq, ok := job.(OnQueuer)
	if !ok {
		oq, ok = d.pop().(OnQueuer)
	}
	if ok {
		store(job, oq.OnQueue()) // want contain
	}
	return nil
}

// PushRecursiveHelper's helper calls itself: the proof does not follow the
// cycle, so its parameter is not held to be the caller's own.
func (d *Driver) PushRecursiveHelper(ctx context.Context, job Job) error {
	if _, err := admitJob(job); err != nil {
		return err
	}
	store(job, recursiveQueue(job, 1))
	return nil
}

func recursiveQueue(job Job, depth int) string {
	if depth > 0 {
		return recursiveQueue(job, depth-1)
	}
	if oq, ok := job.(OnQueuer); ok {
		return oq.OnQueue() // want contain
	}
	return "default"
}
