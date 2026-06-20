package velocity

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/queue/queuetest"
)

// fakeQueueJob is a minimal contract.QueueJob used to exercise WithFakeQueue.
type fakeQueueJob struct {
	name string
}

func (j *fakeQueueJob) Handle() error  { return nil }
func (j *fakeQueueJob) Failed(_ error) {}

// (criterion 1) WithFakeQueue pre-sets the driver and Bootstrap keeps it:
// a.Queue is the exact fake after New(). Mirrors the WithFakeEvents wiring.
func TestWithFakeQueue_PreSetDriverKept(t *testing.T) {
	fake := queuetest.NewFakeQueue()

	a, err := NewTestApp(WithFakeQueue(fake))
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}
	defer a.Shutdown(context.Background())

	if a.Queue != contract.QueueDriver(fake) {
		t.Fatalf("a.Queue = %p, want pre-set fake %p", a.Queue, fake)
	}
	// App embeds *app.Services, so the DI surface providers read must agree.
	if a.Services.Queue != contract.QueueDriver(fake) {
		t.Fatalf("a.Services.Queue = %p, want pre-set fake %p", a.Services.Queue, fake)
	}
}

// (criterion 2) A no-option boot still builds the configured driver; the guard
// only changes behavior when a test pre-sets one.
func TestWithFakeQueue_NoOptionBuildsConfiguredDriver(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}
	defer a.Shutdown(context.Background())

	if a.Queue == nil {
		t.Fatal("a.Queue is nil, want configured memory driver")
	}
	if _, ok := a.Queue.(*queuetest.FakeQueue); ok {
		t.Fatal("a.Queue is a FakeQueue without WithFakeQueue; initQueue was skipped unexpectedly")
	}
}

// (criterion 3) A job pushed through the booted app lands in the fake and
// AssertPushed/AssertPushedOn pass.
func TestWithFakeQueue_PushedJobRecorded(t *testing.T) {
	fake := queuetest.NewFakeQueue()

	a, err := NewTestApp(WithFakeQueue(fake))
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}
	defer a.Shutdown(context.Background())

	if err := a.Queue.PushCtx(context.Background(), &fakeQueueJob{name: "welcome"}, "emails"); err != nil {
		t.Fatalf("PushCtx: %v", err)
	}

	match := func(j contract.QueueJob) bool {
		fj, ok := j.(*fakeQueueJob)
		return ok && fj.name == "welcome"
	}
	fake.AssertPushed(t, match)
	fake.AssertPushedOn(t, "emails", match)
}
