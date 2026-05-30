package postgres

import "testing"

// TestOperatorRegistry_Postgres pins the per-driver registry shape so a future
// change cannot silently widen or shrink the dialect-specific operator surface
// without touching this test.
func TestOperatorRegistry_Postgres(t *testing.T) {
	reg := (&PostgresDriver{}).OperatorRegistry()
	if reg == nil {
		t.Fatal("postgres registry should not be nil")
	}
	want := []string{"@>", "<@", "?", "?|", "?&", "@@", "&&"}
	for _, op := range want {
		if _, ok := reg[op]; !ok {
			t.Errorf("postgres registry missing operator %q", op)
		}
	}
	// Ensure every spec carries a non-empty Template (compile renders it).
	for op, spec := range reg {
		if spec.Template == "" {
			t.Errorf("postgres operator %q has empty Template", op)
		}
		if spec.Op != op {
			t.Errorf("postgres operator %q stored under spec.Op=%q", op, spec.Op)
		}
	}
}
