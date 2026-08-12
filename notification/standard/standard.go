// Package standard aggregates every built-in notification channel so a single
// blank-import wires the full set of factories into the notification registry.
//
//	import _ "github.com/velocitykode/velocity/notification/standard"
//
// Importing this package blank-imports every leaf channel driver
// (broadcast, database, mail, slack), each of which registers its factory
// into the notification registry from its own init() and pulls in the
// notification root and its transitive dependencies. Applications that want
// a smaller dependency footprint should blank-import only the specific
// leaves they need instead.
//
// This aggregator MUST NOT be imported by core, router, or app packages
// because it pulls in driver dependencies.
package standard

import (
	// Leaf channel drivers; each self-registers from its init() and pulls
	// in the notification root transitively.
	_ "github.com/velocitykode/velocity/notification/broadcast"
	_ "github.com/velocitykode/velocity/notification/database"
	_ "github.com/velocitykode/velocity/notification/mail"
	_ "github.com/velocitykode/velocity/notification/slack"
)
