// Fixture: skip inside a subtest closure. The checker walks every
// FuncDecl AND FuncLit, so a skip inside `t.Run(name, func(t *testing.T)
// {...})` must be evaluated against its closure scope — NOT the outer
// function's scope. A guard in the outer scope should NOT suppress an
// unguarded skip inside the inner closure.
package fixture

import (
	"os"
	"testing"
)

func TestSubtestClosureGuardInClosure(t *testing.T) {
	t.Run("guarded", func(t *testing.T) {
		// Guard is inside the closure — valid.
		if os.Getenv("X") == "" {
			t.Skip("X missing")
		}
	})
}

func TestSubtestClosureUnguardedInClosure(t *testing.T) {
	// Outer function has a guard, but the closure below has NO guard.
	// The outer guard doesn't apply to the inner scope — the inner skip
	// MUST be flagged.
	if os.Getenv("OUTER") == "" {
		_ = 0
	}
	t.Run("inner naked", func(t *testing.T) {
		t.Skip("outer guard doesn't reach here")
	})
}
