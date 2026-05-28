package events

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/queue"
)

// nopMemQueueDriver is a minimal queue.Driver used only to drive the race
// test below. The actual queue contents do not matter; what matters is that
// concurrent pushToQueue / ProcessEventListenerJob / SetQueueDriver /
// RegisterListenerFactory calls hit the same shared fields and exercise the
// qmu lock under -race.
type nopMemQueueDriver struct{}

func (nopMemQueueDriver) PushCtx(ctx context.Context, job queue.Job, queueName ...string) error {
	return nil
}
func (nopMemQueueDriver) PushDelayedCtx(ctx context.Context, job queue.Job, delay time.Duration, queueName ...string) error {
	return nil
}
func (nopMemQueueDriver) PopCtx(ctx context.Context, q string) (queue.Job, error) { return nil, nil }
func (nopMemQueueDriver) Size(q string) (int64, error)                            { return 0, nil }
func (nopMemQueueDriver) Clear(q string) error                                    { return nil }
func (nopMemQueueDriver) Failed(job queue.Job, err error, q string) error         { return nil }
func (nopMemQueueDriver) Shutdown(ctx context.Context) error                      { return nil }

// raceQueuedListener implements QueuedListener so QueueIntegratedDispatcher.Dispatch
// takes the queued branch (pushToQueue).
type raceQueuedListener struct{}

func (raceQueuedListener) Handle(ctx context.Context, event interface{}) error { return nil }
func (raceQueuedListener) ShouldQueue() bool                                   { return true }
func (raceQueuedListener) OnConnection() string                                { return "" }
func (raceQueuedListener) OnQueue() string                                     { return "default" }
func (raceQueuedListener) WithDelay() time.Duration                            { return 0 }
func (raceQueuedListener) Tries() int                                          { return 1 }

// TestQueueIntegratedDispatcher_ConcurrentSettersAndReaders is the H-23 fix
// regression test. Multiple goroutines hammer SetQueueDriver,
// RegisterListenerFactory, pushToQueue (via Dispatch), and
// ProcessEventListenerJob concurrently. Before the qmu fix, the race
// detector flagged the unprotected map writes; this test must pass under
// `go test -race`.
func TestQueueIntegratedDispatcher_ConcurrentSettersAndReaders(t *testing.T) {
	// pushToQueue requires both a listener-factory and an event-factory
	// registration before it will enqueue; register both up front so the
	// race-test Dispatch path can actually reach the writer code.
	RegisterListenerFactory("events.raceQueuedListener", func() Listener { return raceQueuedListener{} })
	RegisterEventFactory("string", func() interface{} { s := ""; return &s })
	defer UnregisterListenerFactory("events.raceQueuedListener")
	defer UnregisterEventFactory("string")

	d := NewQueueIntegratedDispatcher()
	d.SetQueueDriver(nopMemQueueDriver{})
	d.Listen("race.event", raceQueuedListener{})

	// Pre-build a marshalled job so the ProcessEventListenerJob path always
	// finds at least one known listener type to bind against. The exact
	// type identity is unimportant for the race assertion; we just need
	// the read path to fire.
	jobBytes, err := json.Marshal(EventListenerJob{
		Event:        json.RawMessage(`"race.event"`),
		EventType:    "string",
		ListenerType: "events.raceQueuedListener",
		MaxRetries:   1,
	})
	if err != nil {
		t.Fatalf("marshal job: %v", err)
	}

	const (
		writers = 8
		readers = 8
		iters   = 200
	)
	var wg sync.WaitGroup
	wg.Add(writers * 2)
	// Writers: SetQueueDriver + RegisterListenerFactory.
	for w := 0; w < writers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				d.SetQueueDriver(nopMemQueueDriver{})
			}
		}()
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				name := "events.raceQueuedListener"
				if w%2 == 0 {
					name = "events.raceQueuedListener.alt"
				}
				d.RegisterListenerFactory(name, func() Listener { return raceQueuedListener{} })
			}
		}(w)
	}

	wg.Add(readers * 2)
	// Readers: Dispatch (pushToQueue) + ProcessEventListenerJob (registry read).
	for r := 0; r < readers; r++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				_ = d.Dispatch(context.Background(), "race.event")
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				_ = d.ProcessEventListenerJob(context.Background(), jobBytes)
			}
		}()
	}

	wg.Wait()
}
