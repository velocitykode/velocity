package notification

import (
	"context"
	"fmt"
	"sync"
	"time"
)

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
	m.eventDispatcher = fn
}

// dispatchEvent dispatches an event if a dispatcher is configured.
func (m *Manager) dispatchEvent(event interface{}) {
	if m.eventDispatcher != nil {
		m.eventDispatcher(event)
	}
}

// Channel returns a registered channel driver by name, creating it from the
// registry if not yet instantiated.
func (m *Manager) Channel(name string) (Channel, error) {
	m.mu.RLock()
	ch, exists := m.channels[name]
	m.mu.RUnlock()

	if exists {
		return ch, nil
	}

	// Create from registry
	ch, err := createChannel(name)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	if existing, ok := m.channels[name]; ok {
		return existing, nil
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
		dispatchNotificationFailed(m.dispatchEvent, ctx, notifiable, notification, channelName, err)
		return fmt.Errorf("notification: channel %q: %w", channelName, err)
	}

	start := time.Now()
	err = ch.Send(ctx, notifiable, notification)
	duration := time.Since(start)

	if err != nil {
		dispatchNotificationFailed(m.dispatchEvent, ctx, notifiable, notification, channelName, err)
		return fmt.Errorf("notification: channel %q: %w", channelName, err)
	}

	dispatchNotificationSent(m.dispatchEvent, ctx, notifiable, notification, channelName, duration)
	return nil
}
