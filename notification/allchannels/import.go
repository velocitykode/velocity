// Package allchannels imports all notification channel drivers to register them.
// Import this package with _ to ensure all channels are registered.
package allchannels

import (
	// Import notification package first
	_ "github.com/velocitykode/velocity/notification"

	// Then import all channel packages — this triggers their init() which registers them.
	// We use blank imports because we just need the side effect of running init().
	_ "github.com/velocitykode/velocity/notification/channels"
)
