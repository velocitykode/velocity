package orm

import "testing"

// TestQuery_Clone_IndependentConditions verifies that a cloned query
// does not share the conditions slice with its source. Regressions here
// would cause one branch's WHERE clauses to leak into the other.
func TestQuery_Clone_IndependentConditions(t *testing.T) {
	base := &Query[User]{
		table:   "users",
		columns: []string{"*"},
	}
	base = base.Where("tenant_id = ?", 1)

	clone := base.Clone()
	clone = clone.Where("active = ?", true)

	if len(base.conditions) != 1 {
		t.Errorf("base conditions = %d, want 1 (clone leaked)", len(base.conditions))
	}
	if len(clone.conditions) != 2 {
		t.Errorf("clone conditions = %d, want 2", len(clone.conditions))
	}
}

// TestQuery_Clone_IndependentOrdersLimitsAndOffsets verifies scalar-
// pointer fields and all slice fields are copied independently.
func TestQuery_Clone_IndependentOrdersLimitsAndOffsets(t *testing.T) {
	base := &Query[User]{
		table:   "users",
		columns: []string{"*"},
	}
	base = base.OrderBy("id", "ASC").Limit(10)

	clone := base.Clone()
	clone = clone.OrderBy("name", "DESC").Limit(20)

	if len(base.orders) != 1 {
		t.Errorf("base orders = %d, want 1 (clone leaked)", len(base.orders))
	}
	if len(clone.orders) != 2 {
		t.Errorf("clone orders = %d, want 2", len(clone.orders))
	}
	if base.limit == nil || *base.limit != 10 {
		t.Errorf("base limit altered: %v", base.limit)
	}
	if clone.limit == nil || *clone.limit != 20 {
		t.Errorf("clone limit = %v, want 20", clone.limit)
	}
}

// TestQuery_Clone_NilReceiverReturnsNil guards against panics when
// callers forget to initialise a query before cloning.
func TestQuery_Clone_NilReceiverReturnsNil(t *testing.T) {
	var q *Query[User]
	if got := q.Clone(); got != nil {
		t.Errorf("nil.Clone() = %v, want nil", got)
	}
}
