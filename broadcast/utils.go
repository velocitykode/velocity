package broadcast

import (
	"crypto/subtle"
	"errors"
	"strings"
)

// SecureCompareToken compares two private/presence channel tokens in
// constant time so custom Authorizer implementations can avoid rolling
// their own equality check (a plain `==` leaks a timing oracle that
// shrinks the brute-force horizon for HMAC-style tokens).
//
// Returns true iff the two byte sequences are identical. Length mismatch
// short-circuits to false without consulting the bytes, which is the
// intended subtle.ConstantTimeCompare behaviour.
func SecureCompareToken(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

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
