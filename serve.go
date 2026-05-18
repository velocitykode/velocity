package velocity

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/orm"
)

// Serve is the single entry point for a Velocity application. If os.Args
// contains a CLI command (len > 1), it delegates to Run() for command dispatch.
// Otherwise it boots the application and starts the HTTP server with signal
// handling and graceful shutdown.
func (a *App) Serve() error {
	// Guarantee the App's shutdown context is cancelled on every exit
	// path from Serve, including the CLI-dispatch path (a.Run) which
	// otherwise would leak the context goroutine created in New().
	// Shutdown() also calls shutdownCancel; double-cancel is a no-op.
	if a.shutdownCancel != nil {
		defer a.shutdownCancel()
	}

	// If CLI arguments are present, delegate to the command dispatcher.
	// This allows main.go to be a single call: v.Providers(...).Routes(...).Serve()
	if len(os.Args) > 1 {
		return a.Run()
	}
	return a.serveHTTP()
}

// serveHTTP boots the application and starts the HTTP server. It is called
// from Serve() when no CLI args are present, and directly from serveRunCmd
// when the hot-reload subprocess entry point ("serve:run") is invoked,
// bypassing Run()'s args-dispatch so the "serve:run" argument does not
// re-enter Serve() → Run() → runCommand → serveRunCmd.run indefinitely.
func (a *App) serveHTTP() error {
	// Test-only fast path: if a hook is installed, short-circuit before
	// touching services or the network. See App.serveHTTPHook.
	if a.serveHTTPHook != nil {
		return a.serveHTTPHook()
	}

	// Wire signal handling before bootstrap. A SIGINT/SIGTERM that
	// arrives during boot is held in the cap-1 buffer until the select
	// below; without this, the signal is delivered to the default
	// handler and terminates the process mid-bootstrap. defer
	// signal.Stop releases the subscription so repeated serveHTTP
	// invocations in the same process do not accumulate subscribers.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(quit)

	if err := a.bootstrap(); err != nil {
		// Bootstrap may have partially wired subsystems (chain providers
		// Register/Boot, middleware, event listeners). Shutdown unwinds
		// every subsystem idempotently, so run it here before returning
		// so nothing is left dangling. Its error is joined onto the
		// bootstrap error so the caller sees both failures.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if sdErr := a.Shutdown(shutdownCtx); sdErr != nil {
			return errors.Join(err, sdErr)
		}
		return err
	}

	addr := ":" + a.config.Port
	a.server = &http.Server{
		Addr:              addr,
		Handler:           a.Router,
		ReadTimeout:       a.config.ReadTimeout,
		ReadHeaderTimeout: a.config.ReadHeaderTimeout,
		WriteTimeout:      a.config.WriteTimeout,
		IdleTimeout:       a.config.IdleTimeout,
		BaseContext:       func(net.Listener) context.Context { return a.shutdownCtx },
	}

	// Pre-commit routes so the tree build and static-route compile do
	// not land on the first request's latency path.
	a.Router.Freeze()

	// Start the scheduler in-process if WithSchedulerInProcess() was set.
	// The loop runs against context.Background() and is brought down by
	// App.Shutdown -> a.Scheduler.Shutdown(ctx), which closes the stop
	// channel and waits on runWg for in-flight jobs. Tying scheduler.Run
	// directly to a.shutdownCtx would race: a.Shutdown cancels
	// shutdownCtx first, scheduler.Run then calls its own Shutdown with
	// the cancelled ctx and returns immediately without draining
	// runWg, leaving in-flight jobs orphaned.
	if a.runScheduler && a.Scheduler != nil {
		async.Go(func() {
			a.Log.Info("Scheduler started in-process")
			if err := a.Scheduler.Run(context.Background()); err != nil && err != context.Canceled {
				a.Log.Error("Scheduler exited with error", "error", err)
			}
		})
	}

	// Start server in a goroutine. async.Go recovers from any panic in
	// ListenAndServe so the main goroutine's select is never starved.
	errCh := make(chan error, 1)
	async.Go(func() {
		a.Log.Info("Velocity server started", "version", a.version, "addr", addr)
		if err := a.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	})

	select {
	case err := <-errCh:
		return fmt.Errorf("velocity: server error: %w", err)
	case sig := <-quit:
		a.Log.Info("Shutting down server", "signal", sig.String())
	}

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return a.Shutdown(ctx)
}

// Shutdown gracefully shuts down all services in reverse initialization order.
// Every subsystem's Shutdown is called even if an earlier one fails; all errors
// are aggregated via errors.Join.
func (a *App) Shutdown(ctx context.Context) error {
	// Cancel the app-wide shutdown context first so any in-flight request
	// handler or background worker observing it (via BaseContext or
	// context.Value from router.Context) can exit promptly.
	if a.shutdownCancel != nil {
		a.shutdownCancel()
	}

	var errs []error
	collect := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	// 1. Stop accepting new connections
	if a.server != nil {
		collect(a.server.Shutdown(ctx))
	}

	// 2. Drain async event dispatcher workers (no-op if running sync).
	if a.Router != nil {
		collect(a.Router.ShutdownEventDispatcher(ctx))
		// 2a. Release the *os.Root file descriptor used by Context.File,
		// Context.Download and Context.SaveFile. Idempotent; safe even
		// if no file root was ever opened.
		collect(a.Router.CloseFileRoot())
	}

	// 3. Stop scheduler
	if a.Scheduler != nil {
		collect(a.Scheduler.Shutdown(ctx))
	}

	// 3a. Stop outbox relay (must run before queue/DB teardown so in-flight
	// dispatches reach the queue and DB before they close).
	if a.outboxRelay != nil {
		collect(a.outboxRelay.Stop(ctx))
	}

	// 4. Close queue driver
	if a.Queue != nil {
		collect(a.Queue.Shutdown(ctx))
	}

	// 5. Close cache connections
	if a.Cache != nil {
		collect(a.Cache.Shutdown(ctx))
	}

	// 5a. Close CSRF store (stops cleanup goroutine).
	if a.CSRF != nil {
		if sd, ok := a.CSRF.(contract.ShutdownAware); ok {
			collect(sd.Shutdown(ctx))
		}
	}

	// 5b. Shutdown mail manager.
	if a.Mail != nil {
		if sd, ok := a.Mail.(contract.ShutdownAware); ok {
			collect(sd.Shutdown(ctx))
		}
	}

	// 5c. Shutdown storage manager.
	if a.Storage != nil {
		if sd, ok := a.Storage.(contract.ShutdownAware); ok {
			collect(sd.Shutdown(ctx))
		}
	}

	// 5d. Shutdown notification manager.
	if a.Notification != nil {
		if sd, ok := a.Notification.(contract.ShutdownAware); ok {
			collect(sd.Shutdown(ctx))
		}
	}

	// 6. Close database connections
	if a.DB != nil {
		collect(a.DB.Shutdown(ctx))
		orm.ResetDefault()
	}

	// 7. Shutdown chain providers in reverse order
	for i := len(a.chainProviders) - 1; i >= 0; i-- {
		collect(a.chainProviders[i].Shutdown(ctx))
	}

	// 8. Shutdown WithProviders providers in reverse registration order
	for i := len(a.providers) - 1; i >= 0; i-- {
		collect(a.providers[i].Shutdown(ctx))
	}

	// 9. Close logger last so all prior steps can still log.
	if a.Log != nil {
		if sd, ok := a.Log.(contract.ShutdownAware); ok {
			collect(sd.Shutdown(ctx))
		} else if closer, ok := a.Log.(interface{ Close() error }); ok {
			collect(closer.Close())
		}
	}

	return errors.Join(errs...)
}
