package console

import (
	"github.com/velocitykode/prism"

	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/nilval"
)

// CacheClear flushes all items from the default cache store.
func CacheClear(c cache.CacheManager) error {
	if nilval.Is(c) {
		prism.Warning("No cache configured")
		return nil
	}

	if err := c.Flush(); err != nil {
		return errchain.Errorf("failed to clear cache: %w", err)
	}

	prism.Success("Cache cleared")
	return nil
}
