package flags

import (
	"context"
	"sync"
)

// MemoryProvider is an in-process Provider backed by a map. It is intended
// for tests, local development, and small single-process apps; production
// systems should use a real adapter (LaunchDarkly, Unleash, PostHog, etc.)
// behind the Provider interface.
type MemoryProvider struct {
	mu    sync.RWMutex
	flags map[string]bool
}

// NewMemoryProvider returns a MemoryProvider seeded with initial. The
// initial map is copied; later mutations to the caller's map do not affect
// the provider. A nil initial is treated as empty.
func NewMemoryProvider(initial map[string]bool) *MemoryProvider {
	m := &MemoryProvider{flags: make(map[string]bool, len(initial))}
	for k, v := range initial {
		m.flags[k] = v
	}
	return m
}

// Enabled reports whether name is on. Unknown flags return false.
func (m *MemoryProvider) Enabled(_ context.Context, name string) bool {
	m.mu.RLock()
	on := m.flags[name]
	m.mu.RUnlock()
	return on
}

// Set toggles a single flag.
func (m *MemoryProvider) Set(name string, on bool) {
	m.mu.Lock()
	m.flags[name] = on
	m.mu.Unlock()
}

// SetAll replaces every flag with the supplied map. The input is copied;
// passing nil clears all flags. Build and swap happen under the same lock
// so a concurrent Set cannot be silently overwritten between the build
// and the swap.
func (m *MemoryProvider) SetAll(values map[string]bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := make(map[string]bool, len(values))
	for k, v := range values {
		next[k] = v
	}
	m.flags = next
}
