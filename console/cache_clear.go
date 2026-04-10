package console

import (
	"fmt"

	"github.com/velocitykode/velocity/cache"
)

// CacheClear flushes all items from the default cache store.
func CacheClear(c *cache.Manager) error {
	if c == nil {
		fmt.Println("No cache configured")
		return nil
	}

	if err := c.Flush(); err != nil {
		return fmt.Errorf("failed to clear cache: %w", err)
	}

	fmt.Println("Cache cleared successfully.")
	return nil
}
