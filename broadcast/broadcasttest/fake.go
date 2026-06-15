package broadcasttest

import (
	"context"
	"fmt"
	"sync"

	"github.com/velocitykode/velocity/broadcast"
)

// FakeBroadcaster is a test double that satisfies [broadcast.Driver] by
// recording every broadcast instead of sending it over a wire. Wire it up with
// broadcast.New(NewFakeBroadcaster()) and assert what the manager sent via the
// Assert* helpers, which return an error in the velocity Assert* style (see
// events.FakeDispatcher) rather than failing a *testing.T directly.
//
// All recorded state is guarded by a sync.RWMutex: record paths take the write
// lock, assertion paths take the read lock. The zero value is not usable; call
// NewFakeBroadcaster.
type FakeBroadcaster struct {
	mu         sync.RWMutex
	broadcasts []recordedBroadcast
}

// recordedBroadcast captures a single broadcast for later assertion. SocketID
// is empty for plain broadcasts and carries the excluded socket for the
// BroadcastExcept* family.
type recordedBroadcast struct {
	Channels []string
	Event    string
	Data     any
	SocketID string
}

// NewFakeBroadcaster returns a ready-to-use FakeBroadcaster.
func NewFakeBroadcaster() *FakeBroadcaster {
	return &FakeBroadcaster{}
}

// Compile-time proof the fake satisfies the driver contract.
var _ broadcast.Driver = (*FakeBroadcaster)(nil)

// BroadcastCtx records a broadcast. Per the [broadcast.Driver] contract it
// honours ctx cancellation: a non-nil ctx whose Err() is already set is
// surfaced before any state is mutated. A nil ctx is treated as no-cancel and
// never panics.
func (f *FakeBroadcaster) BroadcastCtx(ctx context.Context, channels []string, event string, data any) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	f.record(channels, event, data, "")
	return nil
}

// Broadcast is the deprecated non-ctx shim; it delegates to BroadcastCtx with
// context.Background.
//
// Deprecated: use BroadcastCtx with a request-scoped context.Context.
func (f *FakeBroadcaster) Broadcast(channels []string, event string, data any) error {
	return f.BroadcastCtx(context.Background(), channels, event, data)
}

// BroadcastExceptCtx records a broadcast that excludes socketID. Same
// ctx-cancellation contract as BroadcastCtx.
func (f *FakeBroadcaster) BroadcastExceptCtx(ctx context.Context, channels []string, event string, data any, socketID string) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	f.record(channels, event, data, socketID)
	return nil
}

// BroadcastExcept is the deprecated non-ctx shim; it delegates to
// BroadcastExceptCtx with context.Background.
//
// Deprecated: use BroadcastExceptCtx with a request-scoped context.Context.
func (f *FakeBroadcaster) BroadcastExcept(channels []string, event string, data any, socketID string) error {
	return f.BroadcastExceptCtx(context.Background(), channels, event, data, socketID)
}

// GetClients always returns a non-nil empty slice. The fake holds no live
// subscribers, so there is never anyone to report (contract.go:77-83).
func (f *FakeBroadcaster) GetClients(channel string) []string {
	return []string{}
}

// record appends a broadcast under the write lock. A copy of channels is kept
// so a caller mutating its slice after the call cannot corrupt recorded state.
func (f *FakeBroadcaster) record(channels []string, event string, data any, socketID string) {
	cp := append([]string(nil), channels...)
	f.mu.Lock()
	f.broadcasts = append(f.broadcasts, recordedBroadcast{
		Channels: cp,
		Event:    event,
		Data:     data,
		SocketID: socketID,
	})
	f.mu.Unlock()
}

// AssertBroadcast asserts at least one recorded broadcast matches both the
// channel and event matcher. Matchers are either a string (channel matcher
// matches when the recorded Channels slice contains it; event matcher matches
// when the recorded Event equals it) or a func(string) bool predicate. Any
// other type yields a descriptive error and never panics.
func (f *FakeBroadcaster) AssertBroadcast(channelMatcher, eventMatcher any) error {
	chMatch, err := makeMatcher(channelMatcher, "channel")
	if err != nil {
		return err
	}
	evMatch, err := makeMatcher(eventMatcher, "event")
	if err != nil {
		return err
	}

	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, b := range f.broadcasts {
		if matchChannels(b.Channels, chMatch) && evMatch(b.Event) {
			return nil
		}
	}
	return fmt.Errorf("expected a matching broadcast but none of %d recorded broadcasts matched", len(f.broadcasts))
}

// AssertNotBroadcast asserts no recorded broadcast matches both matchers.
func (f *FakeBroadcaster) AssertNotBroadcast(channelMatcher, eventMatcher any) error {
	chMatch, err := makeMatcher(channelMatcher, "channel")
	if err != nil {
		return err
	}
	evMatch, err := makeMatcher(eventMatcher, "event")
	if err != nil {
		return err
	}

	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, b := range f.broadcasts {
		if matchChannels(b.Channels, chMatch) && evMatch(b.Event) {
			return fmt.Errorf("expected no matching broadcast but found event %q on channels %v", b.Event, b.Channels)
		}
	}
	return nil
}

// AssertNothingBroadcast asserts nothing was recorded at all.
func (f *FakeBroadcaster) AssertNothingBroadcast() error {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if n := len(f.broadcasts); n > 0 {
		return fmt.Errorf("expected nothing broadcast but %d broadcasts were recorded", n)
	}
	return nil
}

// AssertBroadcastExcept asserts at least one recorded broadcast matches both
// matchers and excluded exactly socketID.
func (f *FakeBroadcaster) AssertBroadcastExcept(channelMatcher, eventMatcher any, socketID string) error {
	chMatch, err := makeMatcher(channelMatcher, "channel")
	if err != nil {
		return err
	}
	evMatch, err := makeMatcher(eventMatcher, "event")
	if err != nil {
		return err
	}

	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, b := range f.broadcasts {
		if b.SocketID == socketID && matchChannels(b.Channels, chMatch) && evMatch(b.Event) {
			return nil
		}
	}
	return fmt.Errorf("expected a matching broadcast excluding socket %q but none of %d recorded broadcasts matched", socketID, len(f.broadcasts))
}

// Reset clears all recorded broadcasts. It returns an error for symmetry with
// the other Assert* helpers; the error is always nil.
func (f *FakeBroadcaster) Reset() error {
	f.mu.Lock()
	f.broadcasts = nil
	f.mu.Unlock()
	return nil
}

// makeMatcher converts a string or func(string) bool into a predicate. The
// kind label ("channel"/"event") only flavours the error message; an
// unsupported type returns an error rather than panicking.
func makeMatcher(matcher any, kind string) (func(string) bool, error) {
	switch m := matcher.(type) {
	case string:
		return func(s string) bool { return s == m }, nil
	case func(string) bool:
		if m == nil {
			return nil, fmt.Errorf("invalid %s matcher: func(string) bool is nil", kind)
		}
		return m, nil
	default:
		return nil, fmt.Errorf("invalid %s matcher: want string or func(string) bool, got %T", kind, matcher)
	}
}

// matchChannels reports whether any channel in the recorded slice satisfies the
// matcher. The string-matcher case therefore means "Channels contains it".
func matchChannels(channels []string, match func(string) bool) bool {
	for _, c := range channels {
		if match(c) {
			return true
		}
	}
	return false
}
