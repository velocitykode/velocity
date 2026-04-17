package mail

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/velocitykode/velocity/internal/panicerr"
)

// Manager manages multiple mail channels
type Manager struct {
	channels        map[string]Mailer
	mu              sync.RWMutex
	eventDispatcher func(event interface{}) error
}

// SetEventDispatcher sets the function used to dispatch events.
func (m *Manager) SetEventDispatcher(fn func(event interface{}) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.eventDispatcher = fn
}

// dispatchEvent dispatches an event if a dispatcher is configured.
func (m *Manager) dispatchEvent(event interface{}) {
	m.mu.RLock()
	fn := m.eventDispatcher
	m.mu.RUnlock()
	if fn != nil {
		fn(event)
	}
}

// NewManager creates a new mail manager
func NewManager() *Manager {
	return &Manager{
		channels: make(map[string]Mailer),
	}
}

// Channel returns the mailer for the named channel.
// Returns an error if the channel has not been registered via SetChannel.
func (m *Manager) Channel(name string) (Mailer, error) {
	m.mu.RLock()
	mailer, exists := m.channels[name]
	m.mu.RUnlock()

	if exists {
		return mailer, nil
	}

	return nil, fmt.Errorf("velocity/mail: channel %q not configured: %w", name, ErrChannelNotFound)
}

// SetChannel sets a specific mailer for a channel
func (m *Manager) SetChannel(name string, mailer Mailer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.channels[name] = mailer
}

// HasChannel checks if a channel exists
func (m *Manager) HasChannel(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, exists := m.channels[name]
	return exists
}

// GetChannels returns all channel names
func (m *Manager) GetChannels() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.channels))
	for name := range m.channels {
		names = append(names, name)
	}
	return names
}

// Send sends a message using a specific channel
func (m *Manager) Send(ctx context.Context, channel string, msg *Message) error {
	mailer, err := m.Channel(channel)
	if err != nil {
		return fmt.Errorf("velocity/mail: send via %q: %w", channel, err)
	}

	// Extract recipient emails for event dispatching
	toAddresses := msg.GetTo()
	toEmails := make([]string, len(toAddresses))
	for i, addr := range toAddresses {
		toEmails[i] = addr.Email
	}
	subject := msg.GetSubject()

	start := time.Now()
	err = mailer.Send(ctx, msg)
	duration := time.Since(start)

	if err != nil {
		dispatchMailFailed(m.dispatchEvent, ctx, toEmails, subject, channel, err, duration)
		return err
	}

	dispatchMailSent(m.dispatchEvent, ctx, toEmails, subject, channel, duration)
	return nil
}

// Broadcast sends a message to multiple channels
func (m *Manager) Broadcast(ctx context.Context, channels []string, msg *Message) error {
	var wg sync.WaitGroup
	errChan := make(chan error, len(channels))

	for _, channel := range channels {
		wg.Add(1)
		// Recover from panics in Send so one bad channel does not tear
		// down the broadcast fan-out; surface as MailFailed event and
		// an error on errChan.
		go func(ch string) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					err := panicerr.FromRecovered(r)
					toEmails := make([]string, 0, len(msg.GetTo()))
					for _, addr := range msg.GetTo() {
						toEmails = append(toEmails, addr.Email)
					}
					dispatchMailFailed(m.dispatchEvent, ctx, toEmails, msg.GetSubject(), ch, err, 0)
					errChan <- fmt.Errorf("velocity/mail: channel %s panic: %w", ch, err)
				}
			}()
			if err := m.Send(ctx, ch, msg); err != nil {
				errChan <- fmt.Errorf("velocity/mail: channel %s: %w", ch, err)
			}
		}(channel)
	}

	wg.Wait()
	close(errChan)

	// Collect errors
	var errors []error
	for err := range errChan {
		errors = append(errors, err)
	}

	if len(errors) > 0 {
		return fmt.Errorf("velocity/mail: broadcast failed: %v", errors)
	}

	return nil
}

// Shutdown releases any resources held by the mail manager. Mail
// drivers do not hold long-lived connections that need draining, so
// this is a no-op; the context is accepted for interface uniformity
// with other ShutdownAware types.
func (m *Manager) Shutdown(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// RemoveChannel removes a channel
func (m *Manager) RemoveChannel(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.channels, name)
}

// ClearChannels removes all channels
func (m *Manager) ClearChannels() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.channels = make(map[string]Mailer)
}
