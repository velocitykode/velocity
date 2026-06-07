package mailtest

import (
	"context"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// FakeMailer is a test double that records every message passed to Send
// instead of delivering it. It satisfies [contract.Mailer], so a test can
// inject it anywhere a Mailer is expected and then assert on what was sent.
//
// The recorder lives on the fake itself; the sent slice is shared across
// goroutines, so every read and write is guarded by mu.
type FakeMailer struct {
	mu   sync.Mutex
	sent []*contract.Message
}

// NewFakeMailer creates a new FakeMailer for testing.
func NewFakeMailer() *FakeMailer {
	return &FakeMailer{}
}

// Compile-time assertion that FakeMailer satisfies the Mailer contract.
var _ contract.Mailer = (*FakeMailer)(nil)

// Send records the message and returns nil without delivering it.
func (f *FakeMailer) Send(ctx context.Context, msg *contract.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, msg)
	return nil
}

// GetSent returns a defensive copy of all recorded messages.
func (f *FakeMailer) GetSent() []*contract.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	sent := make([]*contract.Message, len(f.sent))
	copy(sent, f.sent)
	return sent
}

// countMatching returns the number of recorded messages satisfying match.
// Caller must hold f.mu.
func (f *FakeMailer) countMatching(match func(*contract.Message) bool) int {
	count := 0
	for _, msg := range f.sent {
		if match == nil || match(msg) {
			count++
		}
	}
	return count
}

// AssertSent asserts that at least one recorded message satisfies match.
func (f *FakeMailer) AssertSent(t *testing.T, match func(*contract.Message) bool) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.countMatching(match) == 0 {
		t.Errorf("expected a matching message to be sent, but none was")
	}
}

// AssertSentTimes asserts that exactly n recorded messages satisfy match.
func (f *FakeMailer) AssertSentTimes(t *testing.T, n int, match func(*contract.Message) bool) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if count := f.countMatching(match); count != n {
		t.Errorf("expected %d matching messages to be sent, got %d", n, count)
	}
}

// AssertNotSent asserts that no recorded message satisfies match.
func (f *FakeMailer) AssertNotSent(t *testing.T, match func(*contract.Message) bool) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if count := f.countMatching(match); count > 0 {
		t.Errorf("expected no matching message to be sent, got %d", count)
	}
}

// AssertNothingSent asserts that no messages were recorded at all.
func (f *FakeMailer) AssertNothingSent(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) > 0 {
		t.Errorf("expected no messages to be sent, got %d", len(f.sent))
	}
}
