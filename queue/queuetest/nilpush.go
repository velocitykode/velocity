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
	for _, j := range jobs {
		for _, p := range pushes {
			t.Run(j.name+"/"+p.name, func(t *testing.T) {
				d := factory(t)
				const q = "q-nil-job"
				ok, err := p.push(d, j.job, q)
				if !ok {
					t.Skip("driver does not implement this push")
				}
				if !errors.Is(err, queue.ErrNilJob) {
					t.Fatalf("%s(nil job) = %v; want queue.ErrNilJob", p.name, err)
				}
				if n, serr := d.Size(q); serr != nil || n != 0 {
					t.Errorf("Size after the refused push = %d, %v; want 0", n, serr)
				}
			})
		}
	}
}
