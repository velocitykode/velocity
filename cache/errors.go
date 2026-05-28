package cache

import (
	"github.com/velocitykode/velocity/cache/drivers"
	"github.com/velocitykode/velocity/contract"
)

var (
	// ErrStoreNotFound is an alias for contract.ErrCacheStoreNotFound.
	// Hoisted to the contract package so callers can errors.Is against
	// the shared identity without importing cache.
	ErrStoreNotFound = contract.ErrCacheStoreNotFound
	// ErrKeyNotFound is an alias for contract.ErrCacheKeyNotFound.
	ErrKeyNotFound = contract.ErrCacheKeyNotFound

	// ErrCannotFlushUnprefixed is re-exported from the drivers package so
	// callers can errors.Is against cache.ErrCannotFlushUnprefixed without
	// having to import the drivers subpackage. Returned by RedisStore.Flush
	// when prefix is empty (preventing accidental wipe of an entire shared
	// Redis DB).
	ErrCannotFlushUnprefixed = drivers.ErrCannotFlushUnprefixed
)
