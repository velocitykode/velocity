package velocity

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"reflect"
	"sync"
	"syscall"
	"time"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/drain"
	"github.com/velocitykode/velocity/internal/eventqueue"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/panicerr"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/queue"
)

// Serve is the single entry point for a Velocity application. If os.Args
// contains a CLI command (len > 1), it delegates to Run() for command dispatch;
// a command that fails is handled, the app is shut down and the process
// exits with the command's code, so Serve does not return then and no
// deferred function of its callers runs (see Run). Otherwise it boots the
// application and starts the HTTP server with signal handling and graceful
// shutdown.
func (a *App) Serve() error {
	// Guarantee the App's shutdown context is cancelled on every exit
	// path from Serve, including the CLI-dispatch path (a.Run) which
	// otherwise would leak the context goroutine created in New().
	// Shutdown() also calls shutdownCancel; double-cancel is a no-op.
	if a.shutdownCancel != nil {
		defer a.shutdownCancel(contract.ErrServerShuttingDown)
	}

	// If CLI arguments are present, delegate to the command dispatcher.
	// This allows main.go to be a single call: v.Modules(...).Routes(...).Serve()
	if len(os.Args) > 1 {
		return a.Run()
	}
	return a.serveHTTP()
}

// serveHTTP boots the application and starts the HTTP server. It is called
// from Serve() when no CLI args are present, and directly from serveRunCmd
// when the hot-reload subprocess entry point ("serve run") is invoked,
// bypassing Run()'s args-dispatch so the "serve run" arguments do not
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
		// Bootstrap may have partially wired subsystems (chain modules
		// Init/Start, middleware, event listeners). Shutdown unwinds
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
	// shutdownCtx before it stops the scheduler, scheduler.Run then
	// calls its own Shutdown with the cancelled ctx and returns
	// immediately without draining runWg, leaving in-flight jobs
	// orphaned.
	//
	// Start-after-teardown race: if ListenAndServe fails fast, the
	// errCh path below calls a.Shutdown before this goroutine may have
	// entered Run. Scheduler.Shutdown marks the scheduler terminated
	// even when it is not yet running, and Run refuses to start once
	// terminated, so a late-scheduled Run cannot begin ticking against
	// already-closed services.
	if a.runScheduler && a.Scheduler != nil {
		async.Go(func() {
			fallbacklog.Write(a.Log, func(l contract.Logger) { l.Info("Scheduler started in-process") })
			if err := a.Scheduler.Run(context.Background()); err != nil && err != context.Canceled {
				fallbacklog.Write(a.Log, func(l contract.Logger) { l.Error("Scheduler exited with error", "error", err) })
			}
		})
	}

	// Start server in a goroutine. async.Go recovers from any panic in
	// ListenAndServe so the main goroutine's select is never starved.
	errCh := make(chan error, 1)
	async.Go(func() {
		fallbacklog.Write(a.Log, func(l contract.Logger) { l.Info("Velocity server started", "version", a.version, "addr", addr) })
		if err := a.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	})

	select {
	case err := <-errCh:
		// ListenAndServe failed (port in use, bad addr, ...). Bootstrap
		// fully wired every subsystem before we got here, so tear it all
		// down just like the bootstrap-failure path above; the shutdown
		// error (if any) is joined onto the server error.
		serveErr := fmt.Errorf("velocity: server error: %w", err)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if sdErr := a.Shutdown(shutdownCtx); sdErr != nil {
			return errors.Join(serveErr, sdErr)
		}
		return serveErr
	case sig := <-quit:
		fallbacklog.Write(a.Log, func(l contract.Logger) { l.Info("Shutting down server", "signal", sig.String()) })
	}

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return a.Shutdown(ctx)
}

// Shutdown gracefully shuts down all services in reverse initialization order:
// HTTP server, event dispatcher and file root, scheduler, outbox relay, then
// chain modules and WithModules modules (reverse registration order),
// then registry components (reverse registration order), then queue, cache,
// CSRF, mail, storage, notification, view, DB, and the logger last. Modules
// tear down before queue/cache/DB because they Init/Start after all core
// services; unwinding them first keeps those services alive for final flushes
// and matches the New() failure-path order. Registry components sweep after
// modules (so a module can flush using a value it registered) and before
// queue/cache/DB (so a component can still reach core services during its own
// teardown); the registry owns teardown of registered values.
// Every subsystem's Shutdown is called even if an earlier one fails; all errors
// are aggregated via errors.Join. Each step runs contained: a step that
// panics (a module's Shutdown, a service's, the logger's) contributes the
// panic as its error, and the steps after it still run.
//
// ctx is the caller's bound. The teardown runs once, in the order above,
// on a goroutine of its own, with ctx handed to every step; Shutdown waits
// for it or for ctx. At ctx it returns ctx.Err() and the teardown goes on
// in the background, still in order, so no service closes beneath a step
// that is still running. A step that never returns leaves every step after
// it unrun. A Shutdown that overlaps or follows the first waits for that
// same teardown, or for its own ctx, and returns its result; none starts a
// second teardown. A Shutdown called from inside the teardown (a module's
// Shutdown, say) would wait on itself: the teardown goes on, and the call
// returns an error at once.
func (a *App) Shutdown(ctx context.Context) error {
	t := &a.teardown
	if t.stops.Nested() {
		return fmt.Errorf("velocity: Shutdown called from the app's own teardown: %w", contract.ErrStopFromOwnWork)
	}
	t.mu.Lock()
	done := t.stops.Ended()
	owner := done == nil
	if owner {
		done = t.stops.Begin()
	}
	t.mu.Unlock()
	if owner {
		async.Go(func() {
			t.stops.Drain(done, func() { t.err = a.teardownSteps(ctx) })
		})
	}
	if err := t.stops.Await(ctx, done, nil); err != nil {
		return err
	}
	return t.err
}

// appTeardown is the one run of App.Shutdown's teardown: the coordinator
// marks the goroutine running it, and err is its result, written before
// the drain closes.
type appTeardown struct {
	mu    sync.Mutex
	stops drain.Coordinator
	err   error
}

// teardownSteps runs every teardown step in order; see Shutdown.
func (a *App) teardownSteps(ctx context.Context) error {
	var errs []error
	collect := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	// step runs one teardown step contained and collects its error; do
	// runs one that returns none.
	step := func(fn func() error) { collect(safeStep(fn)) }
	do := func(fn func()) { step(func() error { fn(); return nil }) }

	// 1. Stop accepting new connections and drain the in-flight requests
	// until they finish or ctx ends. Then cancel the shutdown context,
	// the base context of every request (BaseContext), with the cause
	// contract.ErrServerShuttingDown: a request still running past ctx's
	// deadline sees its context done, and the error boundary answers it
	// 503 (not the empty 200 a client-gone cancel gets).
	if a.server != nil {
		step(func() error { return a.server.Shutdown(ctx) })
	}
	if a.shutdownCancel != nil {
		do(func() { a.shutdownCancel(contract.ErrServerShuttingDown) })
	}

	// 2. Drain async event dispatcher workers (no-op if running sync).
	if a.Router != nil {
		step(func() error { return a.Router.ShutdownEventDispatcher(ctx) })
		// 2a. Release the *os.Root file descriptor used by Context.File,
		// Context.Download and Context.SaveFile. Idempotent; safe even
		// if no file root was ever opened.
		step(a.Router.CloseFileRoot)
	}

	// 3. Stop scheduler
	if a.Scheduler != nil {
		step(func() error { return a.Scheduler.Shutdown(ctx) })
	}

	// 3a. Stop outbox relay (must run before queue/DB teardown so in-flight
	// dispatches reach the queue and DB before they close).
	if a.outboxRelay != nil {
		step(func() error { return a.outboxRelay.Stop(ctx) })
	}

	// 4. Shutdown chain modules in reverse order, then WithModules
	// modules in reverse registration order. Modules Init/Start
	// AFTER all core services, so reverse-order teardown unwinds them
	// first, while queue/cache/DB are still alive for final flushes;
	// this matches the New() failure path, where the module-unwind
	// closure is pushed last onto the cleanup stack and runs first.
	for i := len(a.chainModules) - 1; i >= 0; i-- {
		m := a.chainModules[i]
		step(func() error { return m.Shutdown(ctx) })
	}
	for i := len(a.modules) - 1; i >= 0; i-- {
		m := a.modules[i]
		step(func() error { return m.Shutdown(ctx) })
	}

	// 4a. Sweep registry components in reverse registration order. The
	// registry owns teardown of registered values: a module that
	// registers a value MUST NOT also close it in its own Shutdown (see
	// app.Register). This runs after module Shutdown so a module can
	// flush using a value it registered, and before queue/cache/DB close so
	// a component can still reach core services during its own teardown.
	do(func() { shutdownComponents(ctx, a.Services, collect) })

	// 5. Close queue driver
	if a.Queue != nil {
		step(func() error { return a.Queue.Shutdown(ctx) })
	}

	// 5a. C-03-fb2 HIGH 2: drop any auto-installed batch repository
	// back to the package-init in-memory default so a subsequent
	// velocity.New on the same process installs its own DB-backed
	// repo against the new *sql.DB. User-installed repos
	// (SetDefaultBatchRepository) survive this reset and remain the
	// process default until the user explicitly swaps them.
	//
	// Also clear the process-wide global event dispatcher and the
	// callback queue pointer so they do not retain references to the
	// torn-down services. Both setters accept nil as the "uninstall"
	// signal.
	do(func() {
		queue.ResetAutoInstalledBatchRepository()
		queue.SetGlobalEventDispatcher(nil)
		queue.SetBatchCallbackQueue(nil, "")
	})
	// H-22: clear the queued-listener failure reporter so a new app
	// instance does not inherit a stale callback bound to the
	// torn-down error handler; mirrors the New() failure-path
	// queue cleanup closure. Also drop the queue signing logger
	// installed by initQueue so it does not retain the torn-down
	// logger (nil-safe setter), and the payload encryptor installed
	// when QUEUE_ENCRYPT=true so it does not retain the torn-down
	// app's encryptor.
	do(func() {
		eventqueue.InitializeQueueIntegration(nil, nil, nil)
		queue.SetSigningLogger(nil)
		queue.SetPayloadEncryptor(nil)
	})

	// 6. Close cache connections
	if a.Cache != nil {
		step(func() error { return a.Cache.Shutdown(ctx) })
	}

	// 6a. Close CSRF store (stops cleanup goroutine).
	if a.CSRF != nil {
		if sd, ok := a.CSRF.(contract.ShutdownAware); ok {
			step(func() error { return sd.Shutdown(ctx) })
		}
	}

	// 6b. Shutdown mail manager.
	if a.Mail != nil {
		if sd, ok := a.Mail.(contract.ShutdownAware); ok {
			step(func() error { return sd.Shutdown(ctx) })
		}
	}

	// 6c. Shutdown storage manager.
	if a.Storage != nil {
		if sd, ok := a.Storage.(contract.ShutdownAware); ok {
			step(func() error { return sd.Shutdown(ctx) })
		}
	}

	// 6d. Shutdown notification manager.
	if a.Notification != nil {
		if sd, ok := a.Notification.(contract.ShutdownAware); ok {
			step(func() error { return sd.Shutdown(ctx) })
		}
	}

	// 6e. Shutdown view engine; mirrors the New() failure-path cleanup.
	if a.View != nil {
		if sd, ok := a.View.(contract.ShutdownAware); ok {
			step(func() error { return sd.Shutdown(ctx) })
		}
	}

	// 7. Close database connections
	if a.DB != nil {
		step(func() error { return a.DB.Shutdown(ctx) })
		do(orm.ResetDefault)
	}

	// 8. Release the process-wide state this app installed (the async
	// panic hook, whose reports end in the logger, and the async and trace
	// package loggers): the previous live app's is installed again, or the
	// packages' defaults, and another app's installation is left alone.
	// The release waits on nothing: a line or report already in flight may
	// still reach the logger after it is closed (the built-in file logger
	// sends a late warning or error to the standalone fallback logger).
	// Then close the logger last so all prior steps can still log.
	do(func() { releasePackageState(a) })
	if a.Log != nil {
		if sd, ok := a.Log.(contract.ShutdownAware); ok {
			step(func() error { return sd.Shutdown(ctx) })
		} else if closer, ok := a.Log.(interface{ Close() error }); ok {
			step(closer.Close)
		}
	}

	return errors.Join(errs...)
}

// shutdownComponents sweeps the type-keyed component registry in REVERSE
// registration order, calling Shutdown on every registered value and every
// hook adapter that implements contract.ShutdownAware. Errors are handed to
// collect for aggregation.
//
// Exactly-once: the same instance may be registered under multiple keys
// (concrete + interface opt-in) and may also appear as its own hook. A seen-set
// guards against shutting one instance down more than once across both values
// and hooks. Only comparable dynamic types enter the seen-set; inserting an
// uncomparable type into a map panics, so an uncomparable value falls back to
// per-entry shutdown and may be shut down more than once. The inverse edge
// also exists: dedupe is by interface equality, so two DISTINCT comparable
// non-pointer values whose fields compare equal would falsely dedupe and the
// second would be skipped. Both edges are documented limitations, expected to
// be rare since registered values are typically pointers (always distinct
// unless actually the same instance).
//
// Each Shutdown call is panic-guarded (see safeStep) so a misbehaving
// third-party Close cannot abort the remaining teardown. The
// sweep runs after module Shutdowns and before core services close, so
// hooks may still flush through the queue, cache, or DB.
func shutdownComponents(ctx context.Context, s *app.Services, collect func(error)) {
	if s == nil {
		return
	}

	type entry struct {
		value any
		hooks []any
	}
	var entries []entry
	s.RangeComponents(func(_ app.ComponentKey, v any, hooks []any) bool {
		entries = append(entries, entry{value: v, hooks: hooks})
		return true
	})

	seen := map[any]struct{}{}
	shutdownOne := func(v any) {
		sd, ok := v.(contract.ShutdownAware)
		if !ok {
			return
		}
		if reflect.TypeOf(v).Comparable() {
			if _, dup := seen[v]; dup {
				return
			}
			seen[v] = struct{}{}
		}
		collect(safeStep(func() error { return sd.Shutdown(ctx) }))
	}

	for i := len(entries) - 1; i >= 0; i-- {
		shutdownOne(entries[i].value)
		for _, h := range entries[i].hooks {
			shutdownOne(h)
		}
	}
}

// safeStep runs one teardown step, converting a panic in it into its
// error, so one bad module, service or component cannot abort the rest of
// App.Shutdown.
func safeStep(step func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = panicerr.FromRecovered(r)
		}
	}()
	return step()
}
