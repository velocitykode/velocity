package mail

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// Manager manages multiple mail channels
type Manager struct {
	channels        map[string]Mailer
	mu              sync.RWMutex
	eventDispatcher func(ctx context.Context, event interface{}) error
}

// Manager must satisfy the contract mail manager interface. The assertion
// checks the contract boundary: every method below routes the aliased mail
// API (contract.Mailer, *contract.Message) through the named channels.
var _ contract.MailManager = (*Manager)(nil)

// SetAttachmentRoot registers the process-wide attachment root used by
// Message.AttachFile, equivalent to SetDefaultAttachmentRoot. Exposed as
// a Manager method so application bootstrap code that already holds a
// Manager does not need a separate import path to configure the root.
//
// The caller retains ownership of the *os.Root and is responsible for
// closing it at shutdown.
func (m *Manager) SetAttachmentRoot(root *os.Root) {
	SetDefaultAttachmentRoot(root)
}

// SetEventDispatcher sets the function used to dispatch events.
func (m *Manager) SetEventDispatcher(fn func(ctx context.Context, event interface{}) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.eventDispatcher = fn
}

// dispatchEvent dispatches an event if a dispatcher is configured. The
// caller-supplied ctx is propagated so listeners observe request-scoped
// values.
func (m *Manager) dispatchEvent(ctx context.Context, event interface{}) {
	m.mu.RLock()
	fn := m.eventDispatcher
	m.mu.RUnlock()
	if fn != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		fn(ctx, event)
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
	// A nil message would panic at the GetTo/GetSubject calls below (and in
	// the driver), so reject it up front before the channel lookup.
	if msg == nil {
		return ErrNilMessage
	}

	// Reject messages that accumulated a setter error (CRLF injection in
	// a header, oversized attachment, ...) before any driver sees them.
	if err := msg.Err(); err != nil {
		return err
	}

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
		// Not async.Go: needs channel-scoped recovery so a panic on one
		// mailer surfaces a per-channel MailFailed event and a typed
		// errChan entry rather than the package logger only. The
		// goroutine is bound to wg.Wait() in this call frame.
		go func(ch string) { //safe-goroutine: channel-scoped recovery dispatches MailFailed and errChan entry, see comment above
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

// ShutdownableMailer is implemented by mailers that need to release resources
// (connection pools, background goroutines) when the application shuts down.
// The *http.Client based drivers (Mailgun, Postmark) do not implement this
// today — the idle-connection cleanup is handled by the underlying transport.
type ShutdownableMailer interface {
	Shutdown(ctx context.Context) error
}

// Shutdown tears down per-channel mailers that opt into ShutdownableMailer and
// clears the channel registry. The first error encountered is returned; other
// errors are reported via the event dispatcher so callers can act on partial
// failures without masking them.
func (m *Manager) Shutdown(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	channels := make(map[string]Mailer, len(m.channels))
	for k, v := range m.channels {
		channels[k] = v
	}
	// Clear up front so any in-flight Send() lookups surface ErrChannelNotFound
	// rather than racing with teardown.
	m.channels = make(map[string]Mailer)
	m.mu.Unlock()

	var firstErr error
	for name, mailer := range channels {
		sm, ok := mailer.(ShutdownableMailer)
		if !ok {
			continue
		}
		if err := sm.Shutdown(ctx); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("velocity/mail: shutdown channel %q: %w", name, err)
			}
			m.dispatchEvent(ctx, fmt.Errorf("velocity/mail: shutdown channel %q: %w", name, err))
		}
	}
	return firstErr
}
