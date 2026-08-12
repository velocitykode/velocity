package orm

import "testing"

// assignableModel declares an assignable allowlist. Only "name" may be
// mass-assigned - "role" must be zeroed by applyAssignmentAccessToStruct even
// when the caller pre-populates the struct.
type assignableModel struct {
	Model[assignableModel]
	Name string `orm:"column:name"`
	Role string `orm:"column:role"`
}

func (assignableModel) TableName() string { return "assignable_models" }
func (assignableModel) AssignableFields() []string {
	return []string{"name"}
}

// protectedModel declares a Protected denylist. "role" must be blanked even
// when callers populate it ahead of Create(*T).
type protectedModel struct {
	Model[protectedModel]
	Name string `orm:"column:name"`
	Role string `orm:"column:role"`
}

func (protectedModel) TableName() string { return "protected_models" }
func (protectedModel) ProtectedFields() []string {
	return []string{"role"}
}

type protectedAdminPolicyModel struct {
	Model[protectedAdminPolicyModel]
	Name    string `orm:"column:name"`
	IsAdmin bool   `orm:"column:is_admin"`
	Role    string `orm:"column:role"`
}

func (protectedAdminPolicyModel) TableName() string { return "protected_admin_policy_models" }
func (protectedAdminPolicyModel) ProtectedFields() []string {
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

// TestApplyAssignableToStruct_AssignableAllowlist verifies non-assignable
// fields are zeroed on Create(*T) paths so mass-assignment protection
// matches mapToStruct semantics.
func TestApplyAssignableToStruct_AssignableAllowlist(t *testing.T) {
	m := &assignableModel{
		Name: "alice",
		Role: "admin",
	}
	if err := applyAssignmentAccessToStruct(m); err != nil {
		t.Fatalf("applyAssignmentAccessToStruct: %v", err)
	}
	if m.Name != "alice" {
		t.Errorf("Name zeroed unexpectedly: %q", m.Name)
	}
	if m.Role != "" {
		t.Errorf("Role should be zeroed by Assignable allowlist; got %q", m.Role)
	}
}

// TestApplyAssignableToStruct_ProtectedDenylist verifies protected fields are
// zeroed on Create(*T) even when set by the caller.
func TestApplyAssignableToStruct_ProtectedDenylist(t *testing.T) {
	m := &protectedModel{
		Name: "alice",
		Role: "admin",
	}
	if err := applyAssignmentAccessToStruct(m); err != nil {
		t.Fatalf("applyAssignmentAccessToStruct: %v", err)
	}
	if m.Name != "alice" {
		t.Errorf("Name zeroed unexpectedly: %q", m.Name)
	}
	if m.Role != "" {
		t.Errorf("Role should be zeroed by Protected denylist; got %q", m.Role)
	}
}

// TestApplyAssignableToStruct_NoPolicyNoOp verifies struct without either
// interface is untouched.
type plainModel struct {
	Model[plainModel]
	Name string `orm:"column:name"`
}

func (plainModel) TableName() string { return "plain_models" }

func TestApplyAssignableToStruct_NoPolicyNoOp(t *testing.T) {
	m := &plainModel{Name: "alice"}
	if err := applyAssignmentAccessToStruct(m); err != nil {
		t.Fatalf("applyAssignmentAccessToStruct: %v", err)
	}
	if m.Name != "alice" {
		t.Errorf("Name unexpectedly modified: %q", m.Name)
	}
}

func TestAssignmentAccessAllows_DocumentsMassAssignmentDefaults(t *testing.T) {
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
			name:     "protected_denies_is_admin_attack_column",
			model:    &protectedAdminPolicyModel{},
			fieldKey: "is_admin",
			want:     false,
		},
		{
			name:     "assignable_denies_is_admin_attack_column",
			model:    &assignableModel{},
			fieldKey: "is_admin",
			want:     false,
		},
		{
			name:     "assignable_denies_role_attack_column",
			model:    &assignableModel{},
			fieldKey: "role",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := AccessFor(tt.model)
			if got := policy.Allows(tt.fieldKey); got != tt.want {
				t.Fatalf("AccessFor(%T).Allows(%q) = %v, want %v", tt.model, tt.fieldKey, got, tt.want)
			}
		})
	}
}

func TestAccessFor_UndeclaredModelDeniesByDefault(t *testing.T) {
	policy := AccessFor(&plainModel{})
	if policy.Allows("name") {
		t.Fatal("model without Assignable or Protected should deny every application field by default")
	}
	if !policy.HasAssignable || policy.HasProtected {
		t.Fatalf("undeclared policy should resolve as an empty Assignable allowlist; got HasAssignable=%v HasProtected=%v", policy.HasAssignable, policy.HasProtected)
	}
	if !policy.implicitDeny {
		t.Fatal("undeclared policy should be marked implicitDeny so map paths reject and struct paths no-op")
	}
}

func TestAccessFor_AllowAllColumnsRestoresOpenPolicy(t *testing.T) {
	policy := AccessFor(&openPolicyModel{})
	if !policy.Allows("is_admin") || !policy.Allows("role") {
		t.Fatal("AllowAllColumns opt-in should allow every field")
	}
	if policy.HasAssignable || policy.HasProtected || policy.implicitDeny {
		t.Fatalf("AllowAllColumns opt-in should resolve as the open zero policy; got %+v", policy)
	}
}
