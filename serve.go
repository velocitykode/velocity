package velocity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/orm"
)

// Serve starts the HTTP server with signal handling and graceful shutdown.
func (a *App) Serve() error {
	if err := a.bootstrap(); err != nil {
		return err
	}

	addr := ":" + a.config.Port
	a.server = &http.Server{
		Addr:         addr,
		Handler:      a.Router,
		ReadTimeout:  a.config.ReadTimeout,
		WriteTimeout: a.config.WriteTimeout,
		IdleTimeout:  a.config.IdleTimeout,
	}

	// Pre-commit routes so the tree build and static-route compile do
	// not land on the first request's latency path.
	a.Router.Freeze()

	// Start server in a goroutine. async.Go recovers from any panic in
	// ListenAndServe so the main goroutine's select is never starved.
	errCh := make(chan error, 1)
	async.Go(func() {
		a.Log.Info("Velocity server started", "version", a.version, "addr", addr)
		if err := a.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	})

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

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
	}

	// 3. Stop scheduler
	if a.Scheduler != nil {
		collect(a.Scheduler.Shutdown(ctx))
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
		if shutdowner, ok := a.CSRF.(interface {
			Shutdown(context.Context) error
		}); ok {
			collect(shutdowner.Shutdown(ctx))
		}
	}

	// 5b. Shutdown mail manager.
	if a.Mail != nil {
		if shutdowner, ok := a.Mail.(interface {
			Shutdown(context.Context) error
		}); ok {
			collect(shutdowner.Shutdown(ctx))
		}
	}

	// 5c. Shutdown storage manager.
	if a.Storage != nil {
		if shutdowner, ok := a.Storage.(interface {
			Shutdown(context.Context) error
		}); ok {
			collect(shutdowner.Shutdown(ctx))
		}
	}

	// 5d. Shutdown notification manager.
	if a.Notification != nil {
		if shutdowner, ok := a.Notification.(interface {
			Shutdown(context.Context) error
		}); ok {
			collect(shutdowner.Shutdown(ctx))
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
		if shutdowner, ok := a.Log.(interface{ Shutdown(context.Context) error }); ok {
			collect(shutdowner.Shutdown(ctx))
		} else if closer, ok := a.Log.(interface{ Close() error }); ok {
			collect(closer.Close())
		}
	}

	return errors.Join(errs...)
}
