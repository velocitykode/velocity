package console

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/velocitykode/prism"
	"github.com/velocitykode/velocity/queue"
)

// QueueWorkOptions holds flags for the queue work command.
type QueueWorkOptions struct {
	Queue   string
	Tries   int
	Timeout int
	// Logger is the WorkerLogger that internal worker errors are routed to.
	// When nil, the worker falls back to stderr and emits a per-construction
	// warning. Wire the framework's log.Logger (the interface returned by
	// log.NewLogger) here so worker errors flow through the configured log
	// driver.
	Logger queue.WorkerLogger
	// Dispatcher receives the worker's job lifecycle events (job.processing,
	// job.processed, job.retrying, job.failed). When nil, the worker fires no
	// events. Wire the application's event dispatcher here so listeners see
	// the events and a permanently failed job (job.failed) reaches the error
	// reporters through the dispatcher's failure-report bridge.
	Dispatcher func(ctx context.Context, event interface{}) error
}

// QueueWork starts a queue worker that processes jobs from the given driver.
func QueueWork(driver queue.Driver, opts QueueWorkOptions) error {
	if driver == nil {
		prism.Warning("No queue configured")
		return nil
	}

	w := NewQueueWorker(driver, opts)

	prism.Info(fmt.Sprintf("Processing jobs from queue: %s", queueWorkName(opts)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	prism.Info("Shutting down worker...")
	cancel()
	w.Stop()
	prism.Success("Done")

	return nil
}

// NewQueueWorker builds, without starting it, the worker QueueWork runs:
// it processes jobs from driver on opts.Queue ("default" when empty) by
// calling each job's Handle, with opts.Tries, opts.Timeout, opts.Logger
// and opts.Dispatcher applied when set.
func NewQueueWorker(driver queue.Driver, opts QueueWorkOptions) *queue.Worker {
	handler := func(job queue.Job) error {
		return job.Handle()
	}

	var workerOpts []queue.Option
	if opts.Tries > 0 {
		workerOpts = append(workerOpts, queue.WithMaxRetries(opts.Tries))
	}
	if opts.Timeout > 0 {
		workerOpts = append(workerOpts, queue.WithTimeout(time.Duration(opts.Timeout)*time.Second))
	}
	if opts.Logger != nil {
		workerOpts = append(workerOpts, queue.WithWorkerLogger(opts.Logger))
	}

	w := queue.NewWorker(driver, queueWorkName(opts), handler, workerOpts...)
	if opts.Dispatcher != nil {
		w.SetEventDispatcher(opts.Dispatcher)
	}
	return w
}

// queueWorkName returns the queue a worker built from opts processes.
func queueWorkName(opts QueueWorkOptions) string {
	if opts.Queue == "" {
		return "default"
	}
	return opts.Queue
}
