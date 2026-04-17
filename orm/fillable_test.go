package orm

import "testing"

// fillableModel declares a Fillable allowlist. Only "name" may be
// mass-assigned — "role" must be zeroed by applyFillableToStruct even
// when the caller pre-populates the struct.
type fillableModel struct {
	Model[fillableModel]
	Name string `orm:"column:name"`
	Role string `orm:"column:role"`
}

func (fillableModel) TableName() string { return "fillable_models" }
func (fillableModel) Fillable() []string {
	return []string{"name"}
}

// guardedModel declares a Guarded denylist. "role" must be blanked even
// when callers populate it ahead of Create(*T).
type guardedModel struct {
	Model[guardedModel]
	Name string `orm:"column:name"`
	Role string `orm:"column:role"`
}

func (guardedModel) TableName() string { return "guarded_models" }
func (guardedModel) Guarded() []string {
	return []string{"role"}
}

// TestApplyFillableToStruct_FillableAllowlist verifies non-fillable
// fields are zeroed on Create(*T) paths so mass-assignment protection
// matches mapToStruct semantics.
func TestApplyFillableToStruct_FillableAllowlist(t *testing.T) {
	m := &fillableModel{
		Name: "alice",
		Role: "admin",
	}
	if err := applyFillableToStruct(m); err != nil {
		t.Fatalf("applyFillableToStruct: %v", err)
	}
	if m.Name != "alice" {
		t.Errorf("Name zeroed unexpectedly: %q", m.Name)
	}
	if m.Role != "" {
		t.Errorf("Role should be zeroed by Fillable allowlist; got %q", m.Role)
	}
}

// TestApplyFillableToStruct_GuardedDenylist verifies guarded fields are
// zeroed on Create(*T) even when set by the caller.
func TestApplyFillableToStruct_GuardedDenylist(t *testing.T) {
	m := &guardedModel{
		Name: "alice",
		Role: "admin",
	}
	if err := applyFillableToStruct(m); err != nil {
		t.Fatalf("applyFillableToStruct: %v", err)
	}
	if m.Name != "alice" {
		t.Errorf("Name zeroed unexpectedly: %q", m.Name)
	}
	if m.Role != "" {
		t.Errorf("Role should be zeroed by Guarded denylist; got %q", m.Role)
	}
}

// TestApplyFillableToStruct_NoPolicyNoOp verifies struct without either
// interface is untouched.
type plainModel struct {
	Model[plainModel]
	Name string `orm:"column:name"`
}

func (plainModel) TableName() string { return "plain_models" }

func TestApplyFillableToStruct_NoPolicyNoOp(t *testing.T) {
	m := &plainModel{Name: "alice"}
	if err := applyFillableToStruct(m); err != nil {
		t.Fatalf("applyFillableToStruct: %v", err)
	}
	if m.Name != "alice" {
		t.Errorf("Name unexpectedly modified: %q", m.Name)
	}
}
