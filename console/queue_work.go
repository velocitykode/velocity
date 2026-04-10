package console

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

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
		fmt.Println("No queue configured")
		return nil
	}

	queueName := opts.Queue
	if queueName == "" {
		queueName = "default"
	}

	handler := func(job queue.Job) error {
		return job.Handle()
	}

	var workerOpts []queue.WorkerOption
	if opts.Tries > 0 {
		workerOpts = append(workerOpts, queue.WithMaxRetries(opts.Tries))
	}
	if opts.Timeout > 0 {
		workerOpts = append(workerOpts, queue.WithTimeout(time.Duration(opts.Timeout)*time.Second))
	}

	w := queue.NewWorker(driver, queueName, handler, workerOpts...)

	fmt.Printf("Processing jobs from queue: %s\n", queueName)

	w.Start()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	fmt.Println("\nShutting down worker...")
	w.Stop()
	fmt.Println("Done")

	return nil
}
