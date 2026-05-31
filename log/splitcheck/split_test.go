package splitcheck_test

import (
	"context"
	"errors"
	"testing"

	"github.com/velocitykode/velocity/driverregistry"
	"github.com/velocitykode/velocity/log"
)

// TestLeafDriverUnregisteredWithoutImport verifies that, with no leaf package
// imported, the log root registers only its light drivers (console, null):
// resolving a leaf driver fails with ErrDriverNotFound, while the root drivers
// still resolve. The positive counterpart for file/daily/stack lives in
// log/standard.
func TestLeafDriverUnregisteredWithoutImport(t *testing.T) {
	for _, name := range []string{"file", "daily", "stack"} {
		_, err := log.Drivers().Resolve(context.Background(), name, log.LogConfig{Driver: name})
		if err == nil {
			t.Fatalf("Resolve(%q) must fail when no log leaf is imported", name)
		}
		if !errors.Is(err, driverregistry.ErrDriverNotFound) {
			t.Fatalf("error %v is not ErrDriverNotFound", err)
		}
	}
	// The light root drivers must still resolve so zero-config apps work without
	// importing any leaf.
	for _, name := range []string{"console", "null"} {
		logger, err := log.Drivers().Resolve(context.Background(), name, log.LogConfig{Driver: name})
		if err != nil {
			t.Fatalf("Resolve(%q) must succeed from root-only imports: %v", name, err)
		}
		if logger == nil {
			t.Fatalf("Resolve(%q) returned a nil logger", name)
		}
	}
}
