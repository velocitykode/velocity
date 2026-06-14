package console

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/velocitykode/prism"
	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/scheduler"
)

// ScheduleWork starts the scheduler to run scheduled tasks.
func ScheduleWork(s scheduler.TaskScheduler) error {
	if s == nil {
		prism.Warning("No scheduler configured")
		return nil
	}

	prism.Info("Running scheduled tasks...")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	// async.Go recovers from any panic in Scheduler.Run so the CLI's
	// signal loop always observes a terminal value on errCh.
	async.Go(func() {
		errCh <- s.Run(ctx)
	})

	select {
	case <-quit:
		prism.Info("Shutting down scheduler...")
		cancel()
		<-errCh
		prism.Success("Done")
		return nil
	case err := <-errCh:
		return err
	}
}
