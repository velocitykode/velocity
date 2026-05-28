package storage_test

import (
	"testing"

	"github.com/velocitykode/velocity/storage"
	"github.com/velocitykode/velocity/storage/storagetest"
)

// TestMemoryDriver_Contract runs the storagetest spec against the
// in-process memory driver.
func TestMemoryDriver_Contract(t *testing.T) {
	storagetest.RunDriverContractTests(t, func(t *testing.T) storage.Driver {
		return storage.NewMemoryDriver(storage.DiskConfig{Driver: "memory"})
	})
}

// TestLocalDriver_Contract runs the storagetest spec against the local
// filesystem driver rooted at t.TempDir.
func TestLocalDriver_Contract(t *testing.T) {
	storagetest.RunDriverContractTests(t, func(t *testing.T) storage.Driver {
		return storage.NewLocalDriver(storage.DiskConfig{
			Driver: "local",
			Root:   t.TempDir(),
		})
	})
}
