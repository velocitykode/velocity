package queue

import (
	"context"
	"testing"
	"time"
)

// readProbe is a context that counts the values read from it. Building a
// framework event reads its envelope (the trace ids) from the context, so
// the count tells whether an event was built, on the calling goroutine,
// whatever else the process allocates meanwhile.
type readProbe struct {
	context.Context
	reads int
}

func (p *readProbe) Value(key any) any {
	p.reads++
	return p.Context.Value(key)
}

// TestMemoryDriver_NoDispatcherBuildsNoEvent requires a push to build no
// JobQueued event when no event dispatcher is installed: it reads less from
// its context than the same push with a dispatcher, which receives the one
// JobQueued it builds.
func TestMemoryDriver_NoDispatcherBuildsNoEvent(t *testing.T) {
	reads := func(m *MemoryDriver) int {
		p := &readProbe{Context: context.Background()}
		_ = m.PushCtx(p, &TestJob{ID: "a"})
		return p.reads
	}
	without := reads(NewMemoryDriver())
	m := NewMemoryDriver()
	var queued int
	m.SetEventDispatcher(func(_ context.Context, event interface{}) error {
		if _, ok := event.(*JobQueued); ok {
			queued++
		}
		return nil
	})
	with := reads(m)
	if queued != 1 {
		t.Fatalf("the dispatcher received %d JobQueued, want 1", queued)
	}
	if without >= with {
		t.Errorf("push read its context %d times with no dispatcher and %d with one, want fewer without", without, with)
	}
}

// TestWorker_NoDispatcherBuildsNoEvent requires the job events a worker
// dispatches to build nothing when no event dispatcher is installed: the
// helpers read nothing from the context.
func TestWorker_NoDispatcherBuildsNoEvent(t *testing.T) {
	w := NewWorker(NewMemoryDriver(), "default", func(Job) error { return nil })
	p := &readProbe{Context: context.Background()}
	dispatchJobProcessing(w.jobEventDispatch(), p, "t", "default")
	dispatchJobProcessed(w.jobEventDispatch(), p, "t", "default", time.Millisecond)
	dispatchJobRetrying(w.jobEventDispatch(), p, "t", "default", 1, 3, nil, time.Second)
	if p.reads != 0 {
		t.Errorf("job event helpers read the context %d times with no dispatcher, want 0", p.reads)
	}
}
