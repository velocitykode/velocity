package standard_test

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/log"
	_ "github.com/velocitykode/velocity/log/standard"
)

// TestStandard_RegistersLeafDrivers proves the aggregator's blank-import wires
// the leaf drivers into the canonical log registry: with log/standard imported,
// the "file" and "stack" names resolve to working loggers. The negative half of
// this proof (resolving "file" fails when no leaf is imported) lives in
// log/splitcheck so it runs in a leaf-free test binary.
func TestStandard_RegistersLeafDrivers(t *testing.T) {
	for _, name := range []string{"file", "daily", "stack"} {
		cfg := log.LogConfig{Driver: name, Config: map[string]any{
			"path":  t.TempDir(),
			"stack": []string{"null"},
		}}
		logger, err := log.Drivers().Resolve(context.Background(), name, cfg)
		if err != nil {
			t.Fatalf("Resolve(%q) with log/standard imported: %v", name, err)
		}
		if logger == nil {
			t.Fatalf("Resolve(%q) returned nil logger", name)
		}
	}
}
