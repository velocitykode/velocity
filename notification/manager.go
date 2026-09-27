package notification

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/panicerr"
	"github.com/velocitykode/velocity/trace"
)

// Notifier is the interface satisfied by *Manager. It covers the methods
// used through app.Services and router.Context for sending notifications.
type Notifier = contract.Notifier

// Verify *Manager implements Notifier at compile time.
var _ contract.Notifier = (*Manager)(nil)

// Manager orchestrates sending notifications across multiple channels.
type Manager struct {
	channels        map[string]Channel
	mu              sync.RWMutex
	eventDispatcher func(ctx context.Context, event interface{}) error
	logger          contract.Logger
}

// NewManager creates a new notification manager.
func NewManager() *Manager {
	return &Manager{
		channels: make(map[string]Channel),
	}
}

// SetEventDispatcher sets the function used to dispatch events.
func (m *Manager) SetEventDispatcher(fn func(ctx context.Context, event interface{}) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.eventDispatcher = fn
}

// SetLogger installs the logger the manager writes its own lines to (an
// event dispatch that failed) and hands it to every channel that takes one
// (contract.LoggerAware): the channels registered now, and each channel
// created or set later. Nil restores the framework's standalone fallback
// logger. Safe to call while notifications are sent. The channels are
// handed the logger under the manager's lock, as Channel and SetChannel
// hand it, so concurrent calls leave the manager and every channel on the
// same logger.
func (m *Manager) SetLogger(l contract.Logger) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logger = l
	for _, ch := range m.channels {
		if la, ok := ch.(contract.LoggerAware); ok {
			la.SetLogger(l)
		}
	}
}

var _ contract.LoggerAware = (*Manager)(nil)

// log returns the installed logger, or the fallback logger when none is.
func (m *Manager) log() contract.Logger {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return fallbacklog.Resolve(m.logger)
}

// handLogger gives ch the manager's logger when ch takes one. The caller
// holds m.mu.
func (m *Manager) handLogger(ch Channel) {
	if la, ok := ch.(contract.LoggerAware); ok && m.logger != nil {
		la.SetLogger(m.logger)
	}
}

// getEventDispatcher returns the current event dispatcher under the read lock.
func (m *Manager) getEventDispatcher() func(ctx context.Context, event interface{}) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.eventDispatcher
}

// dispatchEvent dispatches an event if a dispatcher is configured. The
// caller-supplied ctx is propagated so listeners observe request-scoped
// values. Errors from the dispatcher are logged but do not interrupt
// notification delivery.
func (m *Manager) dispatchEvent(ctx context.Context, event interface{}) {
	dispatch := m.getEventDispatcher()
	if dispatch == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := dispatch(ctx, event); err != nil {
		m.log().Warn("velocity/notification: event dispatch failed", "error", err)
	}
}

// Shutdown tears down every channel that implements contract.ShutdownAware and
// clears the channel registry. Channels that hold no long-lived resources do
// not implement the interface and are skipped. Every opted-in channel gets a
// Shutdown attempt even if an earlier one fails, and the errors are aggregated
// via errors.Join so no partial failure is masked. Clearing the registry makes
// a second call a no-op returning nil.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	channels := make(map[string]Channel, len(m.channels))
	for k, v := range m.channels {
		channels[k] = v
	}
	m.channels = make(map[string]Channel)
	m.mu.Unlock()

	var errs []error
	for name, ch := range channels {
		sd, ok := ch.(contract.ShutdownAware)
		if !ok {
			continue
		}
		if err := sd.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("velocity/notification: shutdown channel %q: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

// Channel returns a registered channel driver by name, creating it from the
// registry if not yet instantiated.
func (m *Manager) Channel(name string) (Channel, error) {
	// Fast path: check under read lock.
	m.mu.RLock()
	ch, exists := m.channels[name]
	m.mu.RUnlock()

	if exists {
		return ch, nil
	}

	// Slow path: hold write lock for the entire create-and-store sequence
	// so only one goroutine creates the channel instance.
	m.mu.Lock()
	defer m.mu.Unlock()

	// Re-check - another goroutine may have created it while we waited.
	if ch, exists = m.channels[name]; exists {
		return ch, nil
	}

	ch, err := createChannel(name)
	if err != nil {
		return nil, err
	}

	m.handLogger(ch)
	m.channels[name] = ch
	return ch, nil
}

// SetChannel explicitly sets a channel driver instance. A channel that takes
// a logger is handed the manager's logger when the manager has one.
func (m *Manager) SetChannel(name string, ch Channel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handLogger(ch)
	m.channels[name] = ch
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
