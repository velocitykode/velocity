package console

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/velocitykode/velocity/scheduler"
)

// ScheduleWork starts the scheduler to run scheduled tasks.
func ScheduleWork(s *scheduler.Scheduler) error {
	if s == nil {
		fmt.Println("No scheduler configured")
		return nil
	}

	fmt.Println("Running scheduled tasks...")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.Run(ctx)
	}()

	select {
	case <-quit:
		fmt.Println("\nShutting down scheduler...")
		cancel()
		<-errCh
		fmt.Println("Done")
		return nil
	case err := <-errCh:
		return err
	}
}
