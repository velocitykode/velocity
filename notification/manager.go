package notification

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/buildonce"
	"github.com/velocitykode/velocity/internal/eventemit"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/panicerr"
	"github.com/velocitykode/velocity/internal/teardown"
	"github.com/velocitykode/velocity/trace"
)

// Notifier is the interface satisfied by *Manager. It covers the methods
// used through app.Services and router.Context for sending notifications.
type Notifier = contract.Notifier

// Verify *Manager implements Notifier at compile time.
var _ contract.Notifier = (*Manager)(nil)

// Manager orchestrates sending notifications across multiple channels.
type Manager struct {
	channels map[string]Channel
	mu       sync.RWMutex
	// events holds the event dispatcher and handles a failed dispatch
	// through the manager's logger.
	events eventemit.Emitter
	// logger forwards to the logger the manager writes through. Every
	// channel that takes a logger is handed this forwarder once, so a
	// later SetLogger reaches them all through one atomic store, with no
	// channel called again and no lock held.
	logger fallbacklog.Forwarder
	// handing is set by the first SetLogger with a logger: from then on
	// every channel is handed the forwarder. Before it, a channel keeps a
	// logger of its own.
	handing atomic.Bool
	// builds creates each registered channel once at a time, with no lock
	// held.
	builds buildonce.Group[Channel]
	// generation counts Shutdowns, so a channel created across one is not
	// registered into the emptied manager. Guarded by mu.
	generation uint64
}

// NewManager creates a new notification manager.
func NewManager() *Manager {
	return &Manager{
		channels: make(map[string]Channel),
	}
}

// SetEventDispatcher sets the function used to dispatch events; nil
// removes it. Safe to call while the manager sends.
func (m *Manager) SetEventDispatcher(fn func(ctx context.Context, event interface{}) error) {
	// A failed dispatch is logged through the manager's logger as it is at
	// the time of the failure. Installed here, not in NewManager, so a
	// manager built as a literal gets it too.
	m.events.UseLogger(m.log)
	m.events.Set(fn)
}

// SetLogger installs the logger the manager writes its own lines to (the
// first failed dispatch of each event name) and its channels write
// through. Nil restores the framework's standalone fallback logger. Safe
// to call while notifications are sent.
//
// Every channel that takes a logger (contract.LoggerAware) is handed the
// manager's forwarding logger once, from the first SetLogger with a logger
// on: the channels registered then, and each channel created or set
// later. Until then a channel keeps a logger of its own. A channel whose
// SetLogger panics keeps its own logger, the panic is written as a
// warning, and every other channel is still handed. A replacement is
// one atomic store that every channel sees; no channel's SetLogger runs
// under the manager's lock, so one that calls back into the manager
// cannot deadlock it.
func (m *Manager) SetLogger(l contract.Logger) {
	m.logger.Set(l)
	if l == nil || !m.handing.CompareAndSwap(false, true) {
		return
	}
	// A channel registered after this snapshot sees handing set and hands
	// itself the forwarder (handLogger); one registered before is in the
	// snapshot. A channel in both is handed it twice, which is harmless.
	m.mu.RLock()
	channels := make([]Channel, 0, len(m.channels))
	for _, ch := range m.channels {
		channels = append(channels, ch)
	}
	m.mu.RUnlock()
	for _, ch := range channels {
		m.handTo(ch)
	}
}

var _ contract.LoggerAware = (*Manager)(nil)

// log returns the manager's forwarding logger: the installed logger, or
// the fallback logger when none is.
func (m *Manager) log() contract.Logger {
	return &m.logger
}

// handLogger gives ch the manager's forwarding logger when ch takes one and
// the manager hands it. The caller holds no lock (SetLogger is user code)
// and has registered ch already: a first SetLogger that runs meanwhile
// either finds ch in its snapshot or has set handing before this check,
// so ch is never missed.
func (m *Manager) handLogger(ch Channel) {
	if !m.handing.Load() {
		return
	}
	m.handTo(ch)
}

// handTo hands ch the manager's forwarding logger when ch takes one. A
// channel's SetLogger is user code: a panic in it is contained and written
// as a warning, so the channels after it are still handed the logger.
func (m *Manager) handTo(ch Channel) {
	if la, ok := ch.(contract.LoggerAware); ok {
		m.logger.Hand(la, "velocity/notification: a channel's SetLogger panicked; it keeps its own logger")
	}
}

// dispatchEvent dispatches an event if a dispatcher is configured. The
// caller-supplied ctx is propagated so listeners observe request-scoped
// values. A failed dispatch is counted and its event's first failure
// logged (see internal/eventemit); notification delivery is unaffected.
func (m *Manager) dispatchEvent(ctx context.Context, event interface{}) {
	m.events.Emit(ctx, event)
}

// Shutdown tears down every channel that implements contract.ShutdownAware and
// clears the channel registry. Channels that hold no long-lived resources do
// not implement the interface and are skipped. Every opted-in channel gets a
// Shutdown attempt even if an earlier one fails or panics (a panic is that
// channel's error), and the errors are aggregated via errors.Join so no partial failure is masked. Clearing the registry makes
// a second call a no-op returning nil.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	channels := make(map[string]Channel, len(m.channels))
	for k, v := range m.channels {
		channels[k] = v
	}
	m.channels = make(map[string]Channel)
	m.generation++
	m.mu.Unlock()

	var errs []error
	for name, ch := range channels {
		if err := teardown.Close(ctx, ch); err != nil {
			errs = append(errs, fmt.Errorf("velocity/notification: shutdown channel %q: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

// Channel returns a registered channel driver by name, creating it from the
// registry if not yet instantiated.
//
// The channel is created with no lock held: the registered factory and the
// channel's SetLogger are user code, which may call back into the manager.
// Each name is still created once at a time: concurrent first uses wait
// for that creation, and a lookup of a name from inside its own creation
// returns an error at once. A channel whose creation finishes after a
// Shutdown that began after it started is not registered: it is shut
// down, and the error holds its Shutdown error too. A channel created while
// SetChannel registered another under its name is shut down as well, and
// the lookup returns the one set.
func (m *Manager) Channel(name string) (Channel, error) {
	// Fast path: check under read lock.
	m.mu.RLock()
	ch, exists := m.channels[name]
	m.mu.RUnlock()

	if exists {
		return ch, nil
	}

	ch, err := m.builds.Do(context.Background(), name, func() (Channel, error) {
		return m.createAndRegister(name)
	})
	if err != nil {
		var ce *createError
		if errors.As(err, &ce) {
			return nil, ce.err
		}
		return nil, fmt.Errorf("velocity/notification: channel %q: %w", name, err)
	}
	return ch, nil
}

// createAndRegister creates the channel name and registers it, unless it
// was registered meanwhile or the manager was shut down.
func (m *Manager) createAndRegister(name string) (Channel, error) {
	m.mu.RLock()
	ch, exists := m.channels[name]
	generation := m.generation
	m.mu.RUnlock()
	if exists {
		return ch, nil
	}

	ch, err := createChannel(name)
	if err != nil {
		return nil, &createError{err}
	}

	m.mu.Lock()
	if m.generation != generation {
		m.mu.Unlock()
		return nil, &createError{errors.Join(
			fmt.Errorf("velocity/notification: channel %q: the manager was shut down while the channel was created", name),
			disposeChannel(name, ch),
		)}
	}
	if existing, ok := m.channels[name]; ok {
		// SetChannel registered one meanwhile: it wins, as it would have
		// under the old write lock had it come first. The channel created
		// here is shut down; the lookup succeeds, so a failure to shut it
		// down is written as a warning instead.
		m.mu.Unlock()
		if err := disposeChannel(name, ch); err != nil {
			fallbacklog.Write(m.log(), func(l contract.Logger) {
				l.Warn("velocity/notification: a channel created while another was set under its name failed to shut down", "channel", name, "error", err)
			})
		}
		return existing, nil
	}
	m.channels[name] = ch
	m.mu.Unlock()
	// Handed after it is registered, never before: see handLogger.
	m.handLogger(ch)
	return ch, nil
}

// disposeChannel shuts down a channel the manager created but never
// registered, contained, and returns its error.
func disposeChannel(name string, ch Channel) error {
	if err := teardown.Close(context.Background(), ch); err != nil {
		return fmt.Errorf("velocity/notification: shut down unregistered channel %q: %w", name, err)
	}
	return nil
}

// createError marks an error createAndRegister returned, which Channel
// returns as it is, from one the build group returned (a re-entrant
// lookup), which Channel wraps with the channel's name.
type createError struct{ err error }

func (e *createError) Error() string { return e.err.Error() }
func (e *createError) Unwrap() error { return e.err }

// SetChannel explicitly sets a channel driver instance. A channel that takes
// a logger is handed the manager's forwarding logger when the manager hands
// it (see SetLogger), right after it is registered.
func (m *Manager) SetChannel(name string, ch Channel) {
	m.mu.Lock()
	m.channels[name] = ch
	m.mu.Unlock()
	m.handLogger(ch)
}

// Send delivers a notification to a single notifiable across all channels
// returned by notification.Via().
//
// A single notification identifier is allocated once per Send and
// propagated through ctx via WithNotificationID so every channel
// (mail, database, broadcast, ...) sees the same string. Notifications
// that implement WithID provide their own; otherwise a UUIDv4 is used.
// This is what makes "the email I just received" and "the database row
// I just inserted" correlate by ID.
func (m *Manager) Send(ctx context.Context, notifiable interface{}, notification Notification) error {
	return m.send(ctx, notifiable, notification, nil)
}

// send is Send. When delivery is non-nil, each channel delivery sets it to
// the context of its span before the channel runs, so a caller that
// recovers a panic from a channel can report it under the span that
// channel ran in.
func (m *Manager) send(ctx context.Context, notifiable interface{}, notification Notification, delivery *context.Context) error {
	channels := notification.Via(notifiable)
	if len(channels) == 0 {
		return nil
	}

	if IDFromContext(ctx) == "" {
		id := ""
		if wi, ok := notification.(WithID); ok {
			id = wi.ID()
		}
		if id == "" {
			id = NewID()
		}
		ctx = WithNotificationID(ctx, id)
	}

	var firstErr error
	for _, channelName := range channels {
		if err := m.sendViaChannel(ctx, channelName, notifiable, notification, delivery); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	return firstErr
}

// SendMany delivers a notification to multiple notifiables in parallel.
// Goroutines are spawned via async.Go so panics are recovered and logged
// instead of crashing the process. Failures from individual sends are
// aggregated via errors.Join.
func (m *Manager) SendMany(ctx context.Context, notifiables []interface{}, notification Notification) error {
	if len(notifiables) == 0 {
		return nil
	}

	var (
		wg     sync.WaitGroup
		errsMu sync.Mutex
		errs   []error
	)

	wg.Add(len(notifiables))
	for _, notifiable := range notifiables {
		n := notifiable
		// async.Go recovers runtime panics at the goroutine boundary; the
		// explicit defer below runs first and dispatches a NotificationFailed
		// event plus records the error so SendMany's caller sees the failure.
		async.Go(func() {
			defer wg.Done()
			// delivery is the span of the channel delivery in progress,
			// set before each channel runs.
			var delivery context.Context
			defer func() {
				if r := recover(); r != nil {
					err := panicerr.FromRecovered(r)
					// The panic is the failure of the delivery's span, the
					// one the channel and its nested work ran in. A panic
					// before any delivery opened one is a span of its own
					// under the caller's.
					spanCtx := delivery
					if spanCtx == nil {
						spanCtx, _ = trace.ContinueTrace(ctx)
					}
					m.dispatchNotificationFailed(spanCtx, n, notification, "", err, 0)
					errsMu.Lock()
					errs = append(errs, fmt.Errorf("velocity/notification: send many panic: %w", err))
					errsMu.Unlock()
				}
			}()
			if err := m.send(ctx, n, notification, &delivery); err != nil {
				errsMu.Lock()
				errs = append(errs, err)
				errsMu.Unlock()
			}
		})
	}

	wg.Wait()

	if len(errs) > 0 {
		return fmt.Errorf("velocity/notification: %d of %d sends failed: %w", len(errs), len(notifiables), errors.Join(errs...))
	}
	return nil
}

// sendViaChannel sends a notification through a specific channel. A
// non-nil delivery is set to the delivery's span context (see send).
func (m *Manager) sendViaChannel(ctx context.Context, channelName string, notifiable interface{}, notification Notification, delivery *context.Context) error {
	// Delivery through one channel is its own span under the caller's span
	// (a root span when ctx carries no trace). The channel runs inside it,
	// and NotificationSent / NotificationFailed record it.
	ctx, _ = trace.ContinueTrace(ctx)
	if delivery != nil {
		*delivery = ctx
	}
	ch, err := m.Channel(channelName)
	if err != nil {
		m.dispatchNotificationFailed(ctx, notifiable, notification, channelName, err, 0)
		return fmt.Errorf("velocity/notification: channel %q: %w", channelName, err)
	}

	start := time.Now()
	err = ch.Send(ctx, notifiable, notification)
	duration := time.Since(start)

	if err != nil {
		m.dispatchNotificationFailed(ctx, notifiable, notification, channelName, err, duration)
		return fmt.Errorf("velocity/notification: channel %q: %w", channelName, err)
	}

	m.dispatchNotificationSent(ctx, notifiable, notification, channelName, duration)
	return nil
}
