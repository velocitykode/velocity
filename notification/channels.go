package notification

import (
	"fmt"
	"sync"
)

var (
	channelFactories = make(map[string]func() (Channel, error))
	channelMu        sync.RWMutex
)

// RegisterChannel allows channel drivers to register themselves.
// Typically called from an init() function in each channel driver package.
func RegisterChannel(name string, factory func() (Channel, error)) {
	channelMu.Lock()
	defer channelMu.Unlock()
	channelFactories[name] = factory
}

// createChannel creates a channel driver by name from the registry.
func createChannel(name string) (Channel, error) {
	channelMu.RLock()
	factory, exists := channelFactories[name]
	channelMu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("notification: unsupported channel %q (not registered)", name)
	}

	return factory()
}

// RegisteredChannels returns the names of all registered channel drivers.
func RegisteredChannels() []string {
	channelMu.RLock()
	defer channelMu.RUnlock()

	names := make([]string, 0, len(channelFactories))
	for name := range channelFactories {
		names = append(names, name)
	}
	return names
}
