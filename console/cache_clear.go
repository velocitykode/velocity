package console

import (
	"fmt"

	"github.com/velocitykode/prism"
	"github.com/velocitykode/velocity/cache"
)

// CacheClear flushes all items from the default cache store.
func CacheClear(c cache.CacheManager) error {
	if c == nil {
		prism.Warning("No cache configured")
		return nil
	}

	if err := c.Flush(); err != nil {
		return fmt.Errorf("failed to clear cache: %w", err)
	}

	prism.Success("Cache cleared")
	return nil
}
