package velocity

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

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

	// Start server in a goroutine
	errCh := make(chan error, 1)
	go func() {
		a.Log.Info("Velocity server started", "version", a.version, "addr", addr)
		if err := a.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

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
func (a *App) Shutdown(ctx context.Context) error {
	var firstErr error
	setErr := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	// 1. Stop accepting new connections
	if a.server != nil {
		setErr(a.server.Shutdown(ctx))
	}

	// 2. Drain async event dispatcher workers (no-op if running sync).
	// Done after server shutdown so any final RequestHandled/RequestFailed
	// events from draining in-flight requests still enter the queue.
	if a.Router != nil {
		setErr(a.Router.ShutdownEventDispatcher(ctx))
	}

	// 3. Stop scheduler
	if a.Scheduler != nil {
		setErr(a.Scheduler.Shutdown(ctx))
	}

	// 4. Close queue driver
	if a.Queue != nil {
		setErr(a.Queue.Shutdown(ctx))
	}

	// 5. Close cache connections
	if a.Cache != nil {
		setErr(a.Cache.Shutdown(ctx))
	}

	// 6. Close database connections
	if a.DB != nil {
		setErr(a.DB.Shutdown(ctx))
		orm.ResetDefault()
	}

	// 7. Shutdown chain providers in reverse order (before WithProviders providers)
	for i := len(a.chainProviders) - 1; i >= 0; i-- {
		setErr(a.chainProviders[i].Shutdown(ctx))
	}

	// 8. Shutdown WithProviders providers in reverse registration order
	for i := len(a.providers) - 1; i >= 0; i-- {
		setErr(a.providers[i].Shutdown(ctx))
	}

	// 9. Close logger if it supports it (e.g., file logger) — last, so all
	// prior shutdown steps can still log.
	if a.Log != nil {
		if shutdowner, ok := a.Log.(interface{ Shutdown(context.Context) error }); ok {
			setErr(shutdowner.Shutdown(ctx))
		} else if closer, ok := a.Log.(interface{ Close() error }); ok {
			setErr(closer.Close())
		}
	}

	if firstErr != nil {
		return fmt.Errorf("velocity: shutdown error: %w", firstErr)
	}

	return nil
}
