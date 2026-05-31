// Package standard aggregates every built-in notification channel so a single
// blank-import wires the full set of factories into the notification registry.
//
//	import _ "github.com/velocitykode/velocity/notification/standard"
//
// Importing this package registers the light root driver plus the leaf
// drivers (mail, database, slack, broadcast) and their transitive
// dependencies. Applications that want a smaller dependency footprint
// should blank-import only the specific leaves they need instead.
//
// This aggregator MUST NOT be imported by core, router, or app packages
// because it pulls in provider dependencies.
package standard

import (
	// Light root driver self-registers from notification's init.
	_ "github.com/velocitykode/velocity/notification"
	// Leaf drivers.
	_ "github.com/velocitykode/velocity/notification/broadcast"
	_ "github.com/velocitykode/velocity/notification/database"
	_ "github.com/velocitykode/velocity/notification/mail"
	_ "github.com/velocitykode/velocity/notification/slack"
)
