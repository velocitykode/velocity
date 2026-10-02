package queuetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/velocitykode/velocity/queue"
)

// runNilPushContract is the contract of a driver's pushes for a nil job:
// every push refuses it with queue.ErrNilJob and stores nothing. A nil
// job, untyped or a typed nil pointer, has no state to run: stored, its
// payload would pop as poison or, for a factory that decodes "null",
// as an empty job that runs.
func runNilPushContract(t *testing.T, factory DriverFactory) {
	t.Helper()
	jobs := []struct {
		name string
		job  queue.Job
	}{
		{"untyped nil", nil},
		{"typed nil", (*ContractJob)(nil)},
		{"typed nil OnQueuer", (*nilProbeJob)(nil)},
	}
	pushes := []struct {
		name string
		push func(d queue.Driver, job queue.Job, q string) (bool, error)
	}{
		{"PushCtx", func(d queue.Driver, job queue.Job, q string) (bool, error) {
			return true, d.PushCtx(context.Background(), job, q)
		}},
		{"PushDelayedCtx", func(d queue.Driver, job queue.Job, q string) (bool, error) {
			return true, d.PushDelayedCtx(context.Background(), job, time.Millisecond, q)
		}},
		{"PushIfNotExistsCtx", func(d queue.Driver, job queue.Job, q string) (bool, error) {
			dp, ok := d.(queue.DedupeAwarePusher)
			if !ok {
				return false, nil
			}
			return true, dp.PushIfNotExistsCtx(context.Background(), job, "nil-job-key", q)
		}},
	}
	// The empty name leaves the queue to the job: a push that resolved it
	// before refusing the job would call OnQueue on a nil receiver.
	for _, q := range []string{"q-nil-job", ""} {
		for _, j := range jobs {
			for _, p := range pushes {
				t.Run(j.name+"/"+p.name+"/queue="+q, func(t *testing.T) {
					d := factory(t)
					ok, err := p.push(d, j.job, q)
					if !ok {
						t.Skip("driver does not implement this push")
					}
					if !errors.Is(err, queue.ErrNilJob) {
						t.Fatalf("%s(nil job) = %v; want queue.ErrNilJob", p.name, err)
					}
					for _, name := range []string{"q-nil-job", "default"} {
						if n, serr := d.Size(name); serr != nil || n != 0 {
							t.Errorf("Size(%q) after the refused push = %d, %v; want 0", name, n, serr)
						}
					}
				})
			}
		}
	}
}

// nilProbeJob's optional methods read the receiver, as a user job's do:
// each one panics on a typed nil, so a push that calls one before
// refusing the job panics instead of returning queue.ErrNilJob.
type nilProbeJob struct {
	ID      string
	Queue   string
	BatchID queue.BatchID
}

func (j *nilProbeJob) Handle() error               { return nil }
func (j *nilProbeJob) Failed(error)                {}
func (j *nilProbeJob) OnQueue() string             { return j.Queue }
func (j *nilProbeJob) JobID() string               { return j.ID }
func (j *nilProbeJob) MaxAttempts() int            { return len(j.ID) + 1 }
func (j *nilProbeJob) GetBatchID() queue.BatchID   { return j.BatchID }
func (j *nilProbeJob) SetBatchID(id queue.BatchID) { j.BatchID = id }
