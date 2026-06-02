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

type guardedAdminPolicyModel struct {
	Model[guardedAdminPolicyModel]
	Name    string `orm:"column:name"`
	IsAdmin bool   `orm:"column:is_admin"`
	Role    string `orm:"column:role"`
}

func (guardedAdminPolicyModel) TableName() string { return "guarded_admin_policy_models" }
func (guardedAdminPolicyModel) Guarded() []string {
	return []string{"is_admin"}
}

type strictPolicyModel struct {
	Model[strictPolicyModel]
	Name    string `orm:"column:name"`
	IsAdmin bool   `orm:"column:is_admin"`
	Role    string `orm:"column:role"`
}

func (strictPolicyModel) TableName() string { return "strict_policy_models" }
func (strictPolicyModel) StrictMassAssignment() bool {
	return true
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

func TestFillablePolicyAllows_DocumentsMassAssignmentDefaults(t *testing.T) {
	tests := []struct {
		name     string
		model    any
		fieldKey string
		want     bool
	}{
		{
			name:     "neither_interface_allows_is_admin_attack_column",
			model:    &plainModel{},
			fieldKey: "is_admin",
			want:     true,
		},
		{
			name:     "neither_interface_allows_role_attack_column",
			model:    &plainModel{},
			fieldKey: "role",
			want:     true,
		},
		{
			name:     "guarded_denies_is_admin_attack_column",
			model:    &guardedAdminPolicyModel{},
			fieldKey: "is_admin",
			want:     false,
		},
		{
			name:     "fillable_denies_is_admin_attack_column",
			model:    &fillableModel{},
			fieldKey: "is_admin",
			want:     false,
		},
		{
			name:     "fillable_denies_role_attack_column",
			model:    &fillableModel{},
			fieldKey: "role",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := PolicyFor(tt.model)
			if got := policy.Allows(tt.fieldKey); got != tt.want {
				t.Fatalf("PolicyFor(%T).Allows(%q) = %v, want %v", tt.model, tt.fieldKey, got, tt.want)
			}
		})
	}
}

func TestFillablePolicyAllows_StrictMassAssignmentOptInDeniesByDefault(t *testing.T) {
	policy := PolicyFor(&strictPolicyModel{})
	if policy.Allows("is_admin") {
		t.Fatal("StrictMassAssignment model without Fillable or Guarded should deny is_admin by default")
	}
	if policy.Allows("role") {
		t.Fatal("StrictMassAssignment model without Fillable or Guarded should deny role by default")
	}
	if !policy.HasFillable || policy.HasGuarded {
		t.Fatalf("strict policy should resolve as an empty Fillable allowlist; got HasFillable=%v HasGuarded=%v", policy.HasFillable, policy.HasGuarded)
	}
}
