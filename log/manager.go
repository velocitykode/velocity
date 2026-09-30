package log

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/teardown"
)

// Manager handles multiple logger channels for advanced logging scenarios.
// Each channel can have its own driver configuration (file, console, stack, null)
type Manager struct {
	config   LoggingConfig
	channels map[string]Logger
	mu       sync.RWMutex
	// shutdowns shuts the detached channels down, one run at a time (see
	// Shutdown). Detach is called under mu.
	shutdowns teardown.Children[Logger]
}

// NewManager creates a new logger manager with the given configuration.
// The manager allows different log channels with independent drivers and settings
func NewManager(cfg LoggingConfig) *Manager {
	return &Manager{
		config:   cfg,
		channels: make(map[string]Logger),
	}
}

// Channel returns a logger for the specified channel, creating it if needed.
// Thread-safe and uses double-checked locking for performance
func (m *Manager) Channel(name string) (Logger, error) {
	m.mu.RLock()
	if logger, exists := m.channels[name]; exists {
		m.mu.RUnlock()
		return logger, nil
	}
	m.mu.RUnlock()

	// Create the channel
	m.mu.Lock()

	// Double-check after acquiring write lock
	if logger, exists := m.channels[name]; exists {
		m.mu.Unlock()
		return logger, nil
	}

	channelConfig, exists := m.config.Channels[name]
	if !exists {
		m.mu.Unlock()
		return nil, fmt.Errorf("channel %s not configured", name)
	}

	// Release lock before creating logger to avoid deadlock with stack driver
	m.mu.Unlock()

	logger, err := m.createLogger(channelConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger for channel %s: %w", name, err)
	}

	// Re-acquire lock to store the logger. We released the lock during
	// createLogger (to avoid a stack-driver deadlock), so a concurrent
	// caller may have built and stored the same channel meanwhile. If so,
	// prefer the already-stored instance and discard ours; otherwise two
	// callers would receive different loggers and the loser's resources
	// (e.g. FileLogger descriptors) would leak. Best-effort Shutdown the
	// duplicate outside the lock via the optional Shutdowner interface.
	//
	// Every discarded duplicate, including a *StackLogger, gets a Shutdown
	// attempt. A manager-built stack does not own its children (they are
	// shared, manager-owned channels resolved via m.Channel above), so its
	// Shutdown is non-destructive and will not close those shared children
	// out from under the winning stack.
	m.mu.Lock()
	if existing, exists := m.channels[name]; exists {
		m.mu.Unlock()
		if sd, ok := logger.(Shutdowner); ok {
			_ = sd.Shutdown(context.Background())
		}
		return existing, nil
	}
	m.channels[name] = logger
	m.mu.Unlock()

	return logger, nil
}

// Default returns the default logger channel as configured in LoggingConfig
func (m *Manager) Default() (Logger, error) {
	return m.Channel(m.config.Default)
}

// Shutdown closes every channel logger that implements Shutdowner and clears
// the channel registry. Loggers that hold no resources do not implement the
// interface and are skipped. Every opted-in channel gets a Shutdown attempt
// even if an earlier one fails or panics (a panic is that channel's error),
// and the errors are aggregated via errors.Join so no partial failure is
// masked. The registry is cleared up front.
//
// The children are shut down on a goroutine of the manager's own, as one
// run; Shutdown waits for it or for ctx, whichever ends first, and at ctx
// the run goes on. A Shutdown that overlaps a run waits for the same run,
// and one that finds nothing new returns that run's retained result
// instead of nil, also when a child was published and removed again since.
// A Shutdown's result covers every child published before the call,
// including those an earlier Shutdown was still closing. A Shutdown called
// from a child's Shutdown returns an error wrapping
// contract.ErrStopFromOwnWork at once: it would wait on itself.
//
// A Manager-built stack channel references shared, manager-owned children, so
// its non-destructive Shutdown will not close those children out from under
// their own channel entries (see newManagerStackLogger / StackLogger.Shutdown).
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	children := m.channels
	m.channels = make(map[string]Logger)
	wait := m.shutdowns.Detach(children, func(name string, err error) error {
		return fmt.Errorf("velocity/log: shutdown channel %q: %w", name, err)
	})
	m.mu.Unlock()
	return wait(ctx)
}

// createLogger creates a logger instance based on the channel configuration.
// The Manager wires its multi-channel "stack" semantics differently from
// the standalone log.NewLogger stack: stack channels here resolve other
// Manager channels by name (so a stack can reference a registered
// "single" or "daily" channel without re-deriving the file path), whereas
// NewLogger's stack constructs siblings ad hoc from cfg.Config["stack"].
//
// Non-stack drivers delegate to the canonical driver registry. Channel
// configuration (level, path, max-age) is forwarded through LogConfig so
// the registered factory uses the same fields as standalone NewLogger.
func (m *Manager) createLogger(cfg ChannelConfig) (Logger, error) {
	if cfg.Driver == "stack" {
		channelNames, ok := ToStringSlice(cfg.Options["channels"])
		if !ok {
			return nil, fmt.Errorf("velocity/log: stack driver requires options.channels []string")
		}
		// Aggregate every child-resolve failure with errors.Join so a typo
		// or missing channel in ANY entry takes the whole stack down at
		// boot rather than silently shrinking the fan-out. A degraded
		// stack is a config bug that should surface loudly, not be papered
		// over by surviving children.
		var (
			loggers   []Logger
			childErrs []error
		)
		for _, channelName := range channelNames {
			logger, err := m.Channel(channelName)
			if err != nil {
				childErrs = append(childErrs, fmt.Errorf("velocity/log: stack driver: child %q: %w", channelName, err))
				continue
			}
			loggers = append(loggers, logger)
		}
		if len(childErrs) > 0 {
			return nil, errors.Join(childErrs...)
		}
		if len(loggers) == 0 {
			return nil, fmt.Errorf("velocity/log: stack driver: no valid channels configured")
		}
		return newManagerStackLogger(loggers...), nil
	}

	driverConfig := map[string]any{
		"level": cfg.Level,
	}
	if cfg.Path != "" {
		driverConfig["path"] = cfg.Path
	}
	if cfg.MaxAge > 0 {
		driverConfig["days"] = cfg.MaxAge
	}
	for k, v := range cfg.Options {
		driverConfig[k] = v
	}
	return driverRegistry.Resolve(context.Background(), cfg.Driver, LogConfig{Driver: cfg.Driver, Config: driverConfig})
}

// ToStringSlice coerces a config value into a []string, accepting both a
// native []string and a []any whose every element is a string (the shape
// JSON / env decoding produces). It returns false when v is nil, not a
// slice, or holds a non-string element, so call sites can fail loudly on a
// malformed value instead of silently dropping it.
//
// Exported so leaf driver packages (log/stack) coerce channel lists the
// same way the Manager does, keeping the "channels" config key behaving
// identically across both stack implementations.
func ToStringSlice(v any) ([]string, bool) {
	switch s := v.(type) {
	case []string:
		return s, true
	case []any:
		out := make([]string, 0, len(s))
		for _, e := range s {
			str, ok := e.(string)
			if !ok {
				return nil, false
			}
			out = append(out, str)
		}
		return out, true
	default:
		return nil, false
	}
}

// StackLogger logs to multiple loggers simultaneously.
// Useful for logging to multiple destinations (e.g., file and console)
//
// Each child writes the line in its own containment: a child that panics
// has the line written to the framework's standalone fallback logger
// instead (its warnings and errors reach standard error), with the pairs
// bound on the stack by With, and the children after it still get the
// line.
type StackLogger struct {
	loggers []Logger
	// ownsChildren reports whether this stack created its children
	// exclusively (true for NewStackLogger, used by the standalone stack
	// driver, which resolves fresh siblings) or merely references shared,
	// externally-owned channels (false for a Manager-built stack, whose
	// children are independent manager channels). Only an owning stack
	// cascades Shutdown to its children.
	ownsChildren bool
	// bound are the pairs bound on the stack with With, which each child
	// carries already: a child whose write panics has the line written to
	// the fallback logger with them.
	bound []any
}

// NewStackLogger creates a logger that writes to multiple loggers.
// All provided loggers will receive the same log messages. The returned
// stack owns its children: Shutdown cascades to them.
func NewStackLogger(loggers ...Logger) *StackLogger {
	return &StackLogger{loggers: loggers, ownsChildren: true}
}

// newManagerStackLogger creates a stack whose children are shared,
// manager-owned channels. Its Shutdown is non-destructive: the children are
// shut down via their own channel entries, not cascaded from here.
func newManagerStackLogger(loggers ...Logger) *StackLogger {
	return &StackLogger{loggers: loggers, ownsChildren: false}
}

// Debug logs a debug message to all configured loggers
func (s *StackLogger) Debug(msg string, kvs ...any) {
	for _, logger := range s.loggers {
		fallbacklog.Write(logger, func(l Logger) { l.Debug(msg, kvs...) }, s.bound...)
	}
}

// Info logs an info message to all configured loggers
func (s *StackLogger) Info(msg string, kvs ...any) {
	for _, logger := range s.loggers {
		fallbacklog.Write(logger, func(l Logger) { l.Info(msg, kvs...) }, s.bound...)
	}
}

// Warn logs a warning message to all configured loggers
func (s *StackLogger) Warn(msg string, kvs ...any) {
	for _, logger := range s.loggers {
		fallbacklog.Write(logger, func(l Logger) { l.Warn(msg, kvs...) }, s.bound...)
	}
}

// Error logs an error message to all configured loggers
func (s *StackLogger) Error(msg string, kvs ...any) {
	for _, logger := range s.loggers {
		fallbacklog.Write(logger, func(l Logger) { l.Error(msg, kvs...) }, s.bound...)
	}
}

// Fatal logs a fatal message to all configured loggers
func (s *StackLogger) Fatal(msg string, kvs ...any) {
	for _, logger := range s.loggers {
		fallbacklog.Write(logger, func(l Logger) { l.Fatal(msg, kvs...) }, s.bound...)
	}
}

// With returns a stack over each child's With(kvs...), so every child
// writes kvs before each line's own pairs. The returned stack does not own
// those children: its Shutdown closes nothing.
func (s *StackLogger) With(kvs ...any) Logger {
	children := make([]Logger, len(s.loggers))
	for i, l := range s.loggers {
		children[i] = boundChild(l, kvs)
	}
	bound := newManagerStackLogger(children...)
	// A full slice expression, so a later With on s never writes into
	// this stack's pairs.
	bound.bound = append(s.bound[:len(s.bound):len(s.bound)], kvs...)
	return bound
}

// boundChild returns l.With(kvs...), or the fallback logger bound to kvs
// when l's With panics, so one faulty child neither fails the stack's With
// nor loses the lines meant for it.
func boundChild(l Logger, kvs []any) (child Logger) {
	defer func() {
		if recover() != nil {
			child = fallbacklog.Logger{}.With(kvs...)
		}
	}()
	return l.With(kvs...)
}

// Level reports the stack's effective minimum severity so the below-level
// redaction gate (see redactingLogger / WithRedactors) can skip redaction for
// a record every child would discard. A stack fans each record out to all
// children, so it drops a record only when every child drops it; the stack
// threshold is therefore the minimum of its children's thresholds.
//
// If ANY child does not expose a level, that child's emit-floor is unknown and
// it might emit the record, so the stack reports contract.LogLevelDebug (the
// lowest severity) to disable gating and preserve always-redact for that
// child. An empty stack reports contract.LogLevelDebug for the same
// conservative reason.
func (s *StackLogger) Level() contract.LogLevel {
	min, found := contract.LogLevelDebug, false
	for _, l := range s.loggers {
		lv, ok := l.(leveler)
		if !ok {
			return contract.LogLevelDebug
		}
		if !found || lv.Level() < min {
			min, found = lv.Level(), true
		}
	}
	return min
}

// Shutdown closes all underlying loggers that support it, honoring the
// context deadline. Every child gets a Shutdown attempt even if an earlier
// one fails or panics (a panic is that child's error), and every error is
// returned, joined. A stack that does not own its children (a Manager-built
// stack referencing shared channels) shuts nothing down here, leaving those
// channels to be closed via their own entries.
func (s *StackLogger) Shutdown(ctx context.Context) error {
	if !s.ownsChildren {
		return nil
	}
	var errs []error
	for _, l := range s.loggers {
		if err := teardown.Close(ctx, l); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// NullLogger discards all log messages without any output.
// Useful for testing or disabling logging for specific channels
type NullLogger struct{}

// NewNullLogger creates a logger that silently discards all messages.
// No output is produced regardless of log level
func NewNullLogger() *NullLogger {
	return &NullLogger{}
}

func (n *NullLogger) Debug(msg string, kvs ...any) {}
func (n *NullLogger) Info(msg string, kvs ...any)  {}
func (n *NullLogger) Warn(msg string, kvs ...any)  {}
func (n *NullLogger) Error(msg string, kvs ...any) {}
func (n *NullLogger) Fatal(msg string, kvs ...any) {}

// With returns n: a line with bound pairs is discarded like any other.
func (n *NullLogger) With(kvs ...any) Logger { return n }
