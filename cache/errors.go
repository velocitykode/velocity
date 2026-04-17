package cache

import "errors"

var (
	ErrStoreNotFound = errors.New("velocity/cache: store not found")
	ErrKeyNotFound   = errors.New("velocity/cache: key not found")
)
