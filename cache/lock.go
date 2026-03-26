package cache

import (
	"github.com/velocitykode/velocity/cache/drivers"
)

// Lock is the interface for a cache lock.
type Lock = drivers.Lock

// Lock-related errors re-exported from the drivers package for convenience.
var (
	ErrLockNotAcquired = drivers.ErrLockNotAcquired
	ErrLockTimeout     = drivers.ErrLockTimeout
)
