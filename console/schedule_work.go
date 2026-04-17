package console

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	cli "github.com/velocitykode/velocity-cli"
	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/scheduler"
)

// ScheduleWork starts the scheduler to run scheduled tasks.
func ScheduleWork(s scheduler.TaskScheduler) error {
	if s == nil {
		cli.Warning("No scheduler configured")
		return nil
	}

	cli.Info("Running scheduled tasks...")

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
		cli.Info("Shutting down scheduler...")
		cancel()
		<-errCh
		cli.Success("Done")
		return nil
	case err := <-errCh:
		return err
	}
}
