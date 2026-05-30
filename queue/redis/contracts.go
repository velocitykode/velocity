package redis

import "github.com/velocitykode/velocity/contract"

// Compile-time check that the Redis driver satisfies the framework's
// EventDispatcherAware contract. This assertion lives in the leaf rather
// than the framework core so core never imports the go-redis dependency.
var _ contract.EventDispatcherAware = (*RedisDriver)(nil)
