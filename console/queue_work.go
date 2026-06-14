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

// QueueWorkOptions holds flags for the queue:work command.
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
}

// QueueWork starts a queue worker that processes jobs from the given driver.
func QueueWork(driver queue.Driver, opts QueueWorkOptions) error {
	if driver == nil {
		prism.Warning("No queue configured")
		return nil
	}

	queueName := opts.Queue
	if queueName == "" {
		queueName = "default"
	}

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

	w := queue.NewWorker(driver, queueName, handler, workerOpts...)

	prism.Info(fmt.Sprintf("Processing jobs from queue: %s", queueName))

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
