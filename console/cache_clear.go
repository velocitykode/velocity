package console

import (
	"fmt"

	cli "github.com/velocitykode/velocity-cli"
	"github.com/velocitykode/velocity/cache"
)

// CacheClear flushes all items from the default cache store.
func CacheClear(c cache.CacheManager) error {
	if c == nil {
		cli.Warning("No cache configured")
		return nil
	}

	if err := c.Flush(); err != nil {
		return fmt.Errorf("failed to clear cache: %w", err)
	}

	cli.Success("Cache cleared")
	return nil
}
