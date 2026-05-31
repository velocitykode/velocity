// Package standard aggregates every built-in mail driver so a single
// blank-import wires the full set of factories into the mail registry.
//
//	import _ "github.com/velocitykode/velocity/mail/standard"
//
// Importing this package registers the light root driver (log) plus the
// leaf drivers (mailgun, postmark, local) and their transitive
// dependencies. Applications that want a smaller dependency footprint
// should blank-import only the specific leaves they need instead.
//
// This aggregator MUST NOT be imported by core, router, or app packages
// because it pulls in provider dependencies.
package standard

import (
	// Light root driver (log) self-registers from mail's init.
	_ "github.com/velocitykode/velocity/mail"
	// Leaf drivers.
	_ "github.com/velocitykode/velocity/mail/local"
	_ "github.com/velocitykode/velocity/mail/mailgun"
	_ "github.com/velocitykode/velocity/mail/postmark"
)
