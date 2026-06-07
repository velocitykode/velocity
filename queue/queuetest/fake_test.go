package queuetest

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// fakeJob is a minimal contract.QueueJob used to exercise FakeQueue.
type fakeJob struct {
	name string
}

func (j *fakeJob) Handle() error  { return nil }
func (j *fakeJob) Failed(_ error) {}

// onQueuerJob implements queue.OnQueuer to declare its own target queue.
type onQueuerJob struct {
	name  string
	queue string
}

func (j *onQueuerJob) Handle() error   { return nil }
func (j *onQueuerJob) Failed(_ error)  {}
func (j *onQueuerJob) OnQueue() string { return j.queue }

func matchName(name string) func(contract.QueueJob) bool {
	return func(j contract.QueueJob) bool {
		fj, ok := j.(*fakeJob)
		return ok && fj.name == name
	}
}

func TestFakeQueue(t *testing.T) {
	tests := []struct {
		name  string
		job   *fakeJob
		queue []string // variadic passed to PushCtx
		want  string   // resolved queue name
	}{
		{name: "default queue", job: &fakeJob{name: "a"}, queue: nil, want: "default"},
		{name: "explicit default", job: &fakeJob{name: "b"}, queue: []string{"default"}, want: "default"},
		{name: "named queue", job: &fakeJob{name: "c"}, queue: []string{"emails"}, want: "emails"},
		{name: "another named queue", job: &fakeJob{name: "d"}, queue: []string{"reports"}, want: "reports"},
	}

	ctx := context.Background()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewFakeQueue()
			f.AssertNothingPushed(t)

			if err := f.PushCtx(ctx, tt.job, tt.queue...); err != nil {
				t.Fatalf("PushCtx: %v", err)
			}

			f.AssertPushed(t, matchName(tt.job.name))
			f.AssertPushedOn(t, tt.want, matchName(tt.job.name))
			f.AssertNotPushed(t, matchName("nonexistent"))

			n, err := f.Size(tt.want)
			if err != nil {
				t.Fatalf("Size: %v", err)
			}
			if n != 1 {
				t.Fatalf("Size(%q) = %d, want 1", tt.want, n)
			}

			if err := f.Clear(tt.want); err != nil {
				t.Fatalf("Clear: %v", err)
			}
			if n, _ := f.Size(tt.want); n != 0 {
				t.Fatalf("Size(%q) after Clear = %d, want 0", tt.want, n)
			}
			f.AssertNothingPushed(t)
		})
	}

	t.Run("multiple queues isolated", func(t *testing.T) {
		f := NewFakeQueue()
		if err := f.PushCtx(ctx, &fakeJob{name: "x"}); err != nil {
			t.Fatalf("PushCtx: %v", err)
		}
		if err := f.PushCtx(ctx, &fakeJob{name: "y"}, "emails"); err != nil {
			t.Fatalf("PushCtx: %v", err)
		}
		if err := f.PushDelayedCtx(ctx, &fakeJob{name: "z"}, 0, "emails"); err != nil {
			t.Fatalf("PushDelayedCtx: %v", err)
		}

		f.AssertPushedOn(t, "default", matchName("x"))
		f.AssertPushedOn(t, "emails", matchName("y"))
		f.AssertPushedOn(t, "emails", matchName("z"))

		if n, _ := f.Size("default"); n != 1 {
			t.Fatalf("Size(default) = %d, want 1", n)
		}
		if n, _ := f.Size("emails"); n != 2 {
			t.Fatalf("Size(emails) = %d, want 2", n)
		}

		if len(f.GetPushed()) != 3 {
			t.Fatalf("GetPushed len = %d, want 3", len(f.GetPushed()))
		}

		// Pop drains in FIFO order per queue.
		job, err := f.PopCtx(ctx, "emails")
		if err != nil {
			t.Fatalf("PopCtx: %v", err)
		}
		if !matchName("y")(job) {
			t.Fatalf("PopCtx(emails) returned wrong job: %+v", job)
		}
		if n, _ := f.Size("emails"); n != 1 {
			t.Fatalf("Size(emails) after pop = %d, want 1", n)
		}

		// Pop on an empty queue returns (nil, nil).
		empty, err := f.PopCtx(ctx, "missing")
		if err != nil {
			t.Fatalf("PopCtx(missing): %v", err)
		}
		if empty != nil {
			t.Fatalf("PopCtx(missing) = %v, want nil", empty)
		}
	})

	t.Run("OnQueuer selects queue when none supplied", func(t *testing.T) {
		match := func(j contract.QueueJob) bool {
			oj, ok := j.(*onQueuerJob)
			return ok && oj.name == "report"
		}

		// No explicit queue: the job's OnQueue() target is honored.
		f := NewFakeQueue()
		if err := f.PushCtx(ctx, &onQueuerJob{name: "report", queue: "reports"}); err != nil {
			t.Fatalf("PushCtx: %v", err)
		}
		f.AssertPushedOn(t, "reports", match)
		if n, _ := f.Size("default"); n != 0 {
			t.Fatalf("Size(default) = %d, want 0", n)
		}

		// Explicit queue overrides OnQueue().
		f = NewFakeQueue()
		if err := f.PushDelayedCtx(ctx, &onQueuerJob{name: "report", queue: "reports"}, 0, "urgent"); err != nil {
			t.Fatalf("PushDelayedCtx: %v", err)
		}
		f.AssertPushedOn(t, "urgent", match)
		if n, _ := f.Size("reports"); n != 0 {
			t.Fatalf("Size(reports) = %d, want 0", n)
		}
	})
}
