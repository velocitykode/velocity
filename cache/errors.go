package cache

import (
	"errors"

	"github.com/velocitykode/velocity/cache/drivers"
)

var (
	ErrStoreNotFound = errors.New("velocity/cache: store not found")
	ErrKeyNotFound   = errors.New("velocity/cache: key not found")

	// ErrCannotFlushUnprefixed is re-exported from the drivers package so
	// callers can errors.Is against cache.ErrCannotFlushUnprefixed without
	// having to import the drivers subpackage. Returned by RedisStore.Flush
	// when prefix is empty (preventing accidental wipe of an entire shared
	// Redis DB).
	ErrCannotFlushUnprefixed = drivers.ErrCannotFlushUnprefixed
)
