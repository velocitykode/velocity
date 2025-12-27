// Package alldrivers imports all mail drivers to register them
// Import this package with _ to ensure all drivers are registered
package alldrivers

import (
	// Import mail package first
	_ "github.com/velocitykode/velocity/pkg/mail"

	// Then import all driver packages - this will trigger their init() which registers them
	// We use blank imports because we just need the side effect of running init()
	_ "github.com/velocitykode/velocity/pkg/mail/drivers"
)
