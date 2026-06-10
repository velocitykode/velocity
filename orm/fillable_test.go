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

type openPolicyModel struct {
	Model[openPolicyModel]
	Name    string `orm:"column:name"`
	IsAdmin bool   `orm:"column:is_admin"`
	Role    string `orm:"column:role"`
}

func (openPolicyModel) TableName() string { return "open_policy_models" }
func (openPolicyModel) AllowAllColumns() bool {
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
			name:     "neither_interface_denies_is_admin_attack_column",
			model:    &plainModel{},
			fieldKey: "is_admin",
			want:     false,
		},
		{
			name:     "neither_interface_denies_role_attack_column",
			model:    &plainModel{},
			fieldKey: "role",
			want:     false,
		},
		{
			name:     "allow_all_columns_optin_allows_is_admin",
			model:    &openPolicyModel{},
			fieldKey: "is_admin",
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

func TestPolicyFor_UndeclaredModelDeniesByDefault(t *testing.T) {
	policy := PolicyFor(&plainModel{})
	if policy.Allows("name") {
		t.Fatal("model without Fillable or Guarded should deny every application field by default")
	}
	if !policy.HasFillable || policy.HasGuarded {
		t.Fatalf("undeclared policy should resolve as an empty Fillable allowlist; got HasFillable=%v HasGuarded=%v", policy.HasFillable, policy.HasGuarded)
	}
	if !policy.implicitDeny {
		t.Fatal("undeclared policy should be marked implicitDeny so map paths reject and struct paths no-op")
	}
}

func TestPolicyFor_AllowAllColumnsRestoresOpenPolicy(t *testing.T) {
	policy := PolicyFor(&openPolicyModel{})
	if !policy.Allows("is_admin") || !policy.Allows("role") {
		t.Fatal("AllowAllColumns opt-in should allow every field")
	}
	if policy.HasFillable || policy.HasGuarded || policy.implicitDeny {
		t.Fatalf("AllowAllColumns opt-in should resolve as the open zero policy; got %+v", policy)
	}
}
