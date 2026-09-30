package queue

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/hostile"
)

// stopOnDoneCtx calls fn from its first Done or Value, as a context whose
// methods call back into their owner do.
type stopOnDoneCtx struct {
	context.Context
	once sync.Once
	fn   func()
}

func (c *stopOnDoneCtx) Done() <-chan struct{} {
	c.once.Do(c.fn)
	return c.Context.Done()
}

func (c *stopOnDoneCtx) Value(key any) any {
	c.once.Do(c.fn)
	return c.Context.Value(key)
}

// Start derives the worker's context outside its lifecycle lock: a ctx
// whose Done stops the worker neither deadlocks Start nor stops a worker
// not yet started (Stop before Start is a no-op), and a Stop afterwards
// stops the worker Start started.
func TestWorker_StartWithACtxThatStopsTheWorker(t *testing.T) {
	w := NewWorker(NewMemoryDriver(), "default", func(Job) error { return nil })
	var stopErr error
	ctx := &stopOnDoneCtx{Context: context.Background(), fn: func() { stopErr = w.Stop(context.Background()) }}
	hostile.Within(t, hostile.Deadline, func() { w.Start(ctx) })
	if stopErr != nil {
		t.Fatalf("Stop from Start's ctx = %v, want nil (a no-op before Start)", stopErr)
	}
	var err error
	hostile.Within(t, hostile.Deadline, func() { err = w.Stop(context.Background()) })
	if err != nil {
		t.Fatalf("Stop = %v", err)
	}
}

// A Stop that overlaps a timed-out one waits for the same drain; a Stop
// from the worker's own work signals the stop and returns at once.
func TestWorker_StopsShareOneDrain(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	d := NewMemoryDriver()
	var w *Worker
	nested := make(chan error, 1)
	w = NewWorker(d, "default", func(Job) error {
		once.Do(func() { close(entered) })
		<-release
		nested <- w.Stop(context.Background())
		return nil
	})
	if err := d.PushCtx(context.Background(), &blockingTestJob{}, "default"); err != nil {
		t.Fatalf("push: %v", err)
	}
	w.Start(context.Background())
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Stop(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop with a done ctx while a job runs = %v, want the ctx error", err)
	}
	second := make(chan error, 1)
	go func() { second <- w.Stop(context.Background()) }()
	close(release)
	var err error
	hostile.Within(t, hostile.Deadline, func() { err = <-nested })
	if !errors.Is(err, contract.ErrStopFromOwnWork) {
		t.Fatalf("Stop from the handler = %v, want ErrStopFromOwnWork", err)
	}
	hostile.Within(t, hostile.Deadline, func() { err = <-second })
	if err != nil {
		t.Fatalf("overlapping Stop = %v, want nil once the job finished", err)
	}
}

// blockingTestJob is a job whose handling the worker's handler controls.
type blockingTestJob struct{}

func (*blockingTestJob) Handle() error { return nil }
func (*blockingTestJob) Failed(error)  {}
