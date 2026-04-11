package console

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	cli "github.com/velocitykode/velocity-cli"
	"github.com/velocitykode/velocity/queue"
)

// QueueWorkOptions holds flags for the queue:work command.
type QueueWorkOptions struct {
	Queue   string
	Tries   int
	Timeout int
}

// QueueWork starts a queue worker that processes jobs from the given driver.
func QueueWork(driver queue.Driver, opts QueueWorkOptions) error {
	if driver == nil {
		cli.Warning("No queue configured")
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

	w := queue.NewWorker(driver, queueName, handler, workerOpts...)

	cli.Info(fmt.Sprintf("Processing jobs from queue: %s", queueName))

	w.Start()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	cli.Info("Shutting down worker...")
	w.Stop()
	cli.Success("Done")

	return nil
}
