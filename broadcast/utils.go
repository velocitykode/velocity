package broadcast

import (
	"errors"
	"strings"
)

var (
	// ErrUnauthorized is returned when channel authorization fails
	ErrUnauthorized = errors.New("unauthorized channel access")

	// ErrDriverNotFound is returned when a driver is not found
	ErrDriverNotFound = errors.New("broadcast driver not found")
)

// isPrivateChannel checks if a channel is private
func isPrivateChannel(channel string) bool {
	return strings.HasPrefix(channel, "private-")
}

// isPresenceChannel checks if a channel is a presence channel
func isPresenceChannel(channel string) bool {
	return strings.HasPrefix(channel, "presence-")
}

// isPublicChannel checks if a channel is public
func isPublicChannel(channel string) bool {
	return !isPrivateChannel(channel) && !isPresenceChannel(channel)
}

// parseChannelName extracts the channel name without prefix
func parseChannelName(channel string) string {
	if isPrivateChannel(channel) {
		return strings.TrimPrefix(channel, "private-")
	}
	if isPresenceChannel(channel) {
		return strings.TrimPrefix(channel, "presence-")
	}
	return channel
}
