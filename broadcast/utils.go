package broadcast

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"

	"github.com/velocitykode/velocity/contract"
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
	// ErrUnauthorized is returned when channel authorization fails.
	// Carries the velocity/broadcast: prefix used by every other sentinel
	// in the framework (drive-by fix in the API-surface sweep; older
	// versions returned an unprefixed "unauthorized channel access").
	ErrUnauthorized = errors.New("velocity/broadcast: unauthorized channel access")

	// ErrDriverNotFound is an alias for contract.ErrBroadcastDriverNotFound.
	// Hoisted so callers can errors.Is against the shared identity without
	// importing broadcast.
	ErrDriverNotFound = contract.ErrBroadcastDriverNotFound

	// ErrLeaveUnsupported is returned by BroadcastManager.Leave when the
	// configured driver does not implement the Unsubscriber capability. It
	// wraps errors.ErrUnsupported so callers can errors.Is either this
	// sentinel or the standard unsupported-operation marker.
	ErrLeaveUnsupported = fmt.Errorf("velocity/broadcast: leave unsupported by driver: %w", errors.ErrUnsupported)
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
