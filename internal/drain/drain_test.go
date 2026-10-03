package drain_test

import (
	"os/exec"
	"strings"
	"testing"
)

// internal/drain sits under the router's graph (through the scheduler),
// so it imports only the standard library and leaves the router already
// depends on: contract, internal/errchain, internal/goroutine,
// internal/panicerr and async.
func TestDrainImportsOnlyItsLeaves(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not on PATH")
	}
	out, err := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	allowed := map[string]bool{
		"github.com/velocitykode/velocity/internal/goroutine": true,
		"github.com/velocitykode/velocity/async":              true,
		"github.com/velocitykode/velocity/contract":           true,
		"github.com/velocitykode/velocity/internal/errchain":  true,
		"github.com/velocitykode/velocity/internal/panicerr":  true,
	}
	for _, imp := range strings.Fields(string(out)) {
		if strings.Contains(imp, ".") && !allowed[imp] {
			t.Errorf("internal/drain imports %s; it may import only the standard library, contract, internal/errchain, internal/goroutine, internal/panicerr and async", imp)
		}
	}
}
