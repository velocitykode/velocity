package s3

import (
	"context"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/storage"
)

// TestS3RegistryResolves asserts the "s3" factory self-registers when this
// leaf package is imported, so storage.Drivers().Resolve can build it. The
// empty config trips the factory's "region is required" validation before
// any AWS I/O, which proves the factory is present (a missing registration
// would surface a driver-not-found error instead).
func TestS3RegistryResolves(t *testing.T) {
	_, err := storage.Drivers().Resolve(context.Background(), "s3", storage.DiskConfig{Driver: "s3"})
	if err == nil {
		t.Fatal("expected s3 factory validation error, got nil")
	}
	if !strings.Contains(err.Error(), "region is required") {
		t.Fatalf("expected s3 factory validation error, got %v", err)
	}
}
