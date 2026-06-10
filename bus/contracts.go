package bus

import "github.com/velocitykode/velocity/contract"

// Compile-time check that the Bus satisfies the framework's
// EventDispatcherAware contract, so bootstrap extension wiring
// (RegisterExtension + EventDispatcherAware type assertion) picks it up.
var _ contract.EventDispatcherAware = (*Bus)(nil)
