package bus

import "github.com/velocitykode/velocity/contract"

// Compile-time check that the Bus satisfies the framework's
// EventDispatcherAware contract. An app registers a bus with the typed
// call app.Register[*bus.Bus](s, b); wireInstanceEvents then picks it up
// via contract.EventDispatcherAware during the registry component sweep.
var _ contract.EventDispatcherAware = (*Bus)(nil)
