// Package leaf stands in for a leaf driver outside the queue package.
package leaf

import (
	"context"

	"example.com/m/contract"
	"example.com/m/queue"
)

type Driver struct{}

// PushCtx admits through the exported form: fine.
func (d *Driver) PushCtx(ctx context.Context, job contract.QueueJob, queueName ...string) error {
	name, err := queue.AdmitJob(job, queueName...)
	if err != nil {
		return err
	}
	_ = name
	return nil
}

// PushDelayedCtx admits through a function of its own, not the queue's.
func (d *Driver) PushDelayedCtx(ctx context.Context, job queue.Job) error {
	_, err := admitJob(job) // want admission
	return err
}

func admitJob(job queue.Job) (string, error) { return queue.AdmitJob(job) }

func (d *Driver) pop() queue.Job { return nil }

// Settle reads a popped job's batch id through the queue package's
// interface with no recover: the leaf is in the contain rule's scope.
func (d *Driver) Settle() string {
	if bj, ok := d.pop().(queue.Batchable); ok {
		return bj.GetBatchID() // want contain
	}
	return ""
}

// SettleContained defers the recover: fine.
func (d *Driver) SettleContained() (id string) {
	defer func() { _ = recover() }()
	if bj, ok := d.pop().(queue.Batchable); ok {
		return bj.GetBatchID()
	}
	return ""
}
