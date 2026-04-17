package notification

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// Notifier is the interface satisfied by *Manager. It covers the methods
// used through app.Services and router.Context for sending notifications.
type Notifier interface {
	Send(ctx context.Context, notifiable interface{}, notification Notification) error
	SendMany(ctx context.Context, notifiables []interface{}, notification Notification) error
	Channel(name string) (Channel, error)
	SetChannel(name string, ch Channel)
	SetEventDispatcher(fn func(event interface{}) error)
}

// Verify *Manager implements Notifier at compile time.
var _ Notifier = (*Manager)(nil)

// Manager orchestrates sending notifications across multiple channels.
type Manager struct {
	channels        map[string]Channel
	mu              sync.RWMutex
	eventDispatcher func(event interface{}) error
}

// NewManager creates a new notification manager.
func NewManager() *Manager {
	return &Manager{
		channels: make(map[string]Channel),
	}
}

// SetEventDispatcher sets the function used to dispatch events.
func (m *Manager) SetEventDispatcher(fn func(event interface{}) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.eventDispatcher = fn
}

// getEventDispatcher returns the current event dispatcher under the read lock.
func (m *Manager) getEventDispatcher() func(event interface{}) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.eventDispatcher
}

// dispatchEvent dispatches an event if a dispatcher is configured.
// Errors from the dispatcher are logged but do not interrupt notification delivery.
func (m *Manager) dispatchEvent(event interface{}) {
	dispatch := m.getEventDispatcher()
	if dispatch == nil {
		return
	}
	if err := dispatch(event); err != nil {
		log.Printf("[notification] event dispatch error: %v", err)
	}
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

	// Re-check — another goroutine may have created it while we waited.
	if ch, exists = m.channels[name]; exists {
		return ch, nil
	}

	ch, err := createChannel(name)
	if err != nil {
		return nil, err
	}

	m.channels[name] = ch
	return ch, nil
}

// SetChannel explicitly sets a channel driver instance.
func (m *Manager) SetChannel(name string, ch Channel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.channels[name] = ch
}

// Send delivers a notification to a single notifiable across all channels
// returned by notification.Via().
func (m *Manager) Send(ctx context.Context, notifiable interface{}, notification Notification) error {
	channels := notification.Via(notifiable)
	if len(channels) == 0 {
		return nil
	}

	var firstErr error
	for _, channelName := range channels {
		if err := m.sendViaChannel(ctx, channelName, notifiable, notification); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	return firstErr
}

// SendMany delivers a notification to multiple notifiables.
func (m *Manager) SendMany(ctx context.Context, notifiables []interface{}, notification Notification) error {
	var (
		wg      sync.WaitGroup
		errChan = make(chan error, len(notifiables))
	)

	for _, notifiable := range notifiables {
		wg.Add(1)
		go func(n interface{}) {
			defer wg.Done()
			if err := m.Send(ctx, n, notification); err != nil {
				errChan <- err
			}
		}(notifiable)
	}

	wg.Wait()
	close(errChan)

	var errors []error
	for err := range errChan {
		errors = append(errors, err)
	}

	if len(errors) > 0 {
		return fmt.Errorf("notification: %d of %d sends failed: %v", len(errors), len(notifiables), errors[0])
	}

	return nil
}

// sendViaChannel sends a notification through a specific channel.
func (m *Manager) sendViaChannel(ctx context.Context, channelName string, notifiable interface{}, notification Notification) error {
	ch, err := m.Channel(channelName)
	if err != nil {
		m.dispatchEvent(buildNotificationFailed(ctx, notifiable, notification, channelName, err))
		return fmt.Errorf("notification: channel %q: %w", channelName, err)
	}

	start := time.Now()
	err = ch.Send(ctx, notifiable, notification)
	duration := time.Since(start)

	if err != nil {
		m.dispatchEvent(buildNotificationFailed(ctx, notifiable, notification, channelName, err))
		return fmt.Errorf("notification: channel %q: %w", channelName, err)
	}

	m.dispatchEvent(buildNotificationSent(ctx, notifiable, notification, channelName, duration))
	return nil
}
