package resource

import (
	"testing"
)

// --- test types ---

type testUser struct {
	ID    int
	Name  string
	Email string
	Admin bool
}

func (u testUser) ToResource() map[string]any {
	return map[string]any{
		"id":    u.ID,
		"name":  u.Name,
		"email": u.Email,
	}
}

type testUserWithConditionals struct {
	ID      int
	Name    string
	Email   string
	Admin   bool
	Phone   *string
	IsAdmin bool
}

func (u testUserWithConditionals) ToResource() map[string]any {
	m := map[string]any{
		"id":   u.ID,
		"name": u.Name,
	}
	if k, v, ok := When(u.IsAdmin, "role", "admin"); ok {
		m[k] = v
	}
	if k, v, ok := WhenNotNil("phone", u.Phone); ok {
		m[k] = v
	}
	return m
}

// --- test paginator ---

type testPaginator struct {
	total       int
	perPage     int
	currentPage int
	items       any
}

func (p testPaginator) Total() int       { return p.total }
func (p testPaginator) PerPage() int     { return p.perPage }
func (p testPaginator) CurrentPage() int { return p.currentPage }
func (p testPaginator) Items() any       { return p.items }

// --- tests ---

func TestToResource(t *testing.T) {
	user := testUser{ID: 1, Name: "Alice", Email: "alice@example.com", Admin: true}
	result := user.ToResource()

	if result["id"] != 1 {
		t.Errorf("expected id=1, got %v", result["id"])
	}
	if result["name"] != "Alice" {
		t.Errorf("expected name=Alice, got %v", result["name"])
	}
	if result["email"] != "alice@example.com" {
		t.Errorf("expected email=alice@example.com, got %v", result["email"])
	}
	// Admin should not be in the resource (not included in ToResource)
	if _, exists := result["admin"]; exists {
		t.Error("expected admin field to be excluded from resource")
	}
}

func TestWhen_True(t *testing.T) {
	key, value, ok := When(true, "role", "admin")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if key != "role" || value != "admin" {
		t.Errorf("expected role=admin, got %s=%v", key, value)
	}
}

func TestWhen_False(t *testing.T) {
	_, _, ok := When(false, "role", "admin")
	if ok {
		t.Fatal("expected ok=false")
	}
}

func TestWhenNotNil_NonNil(t *testing.T) {
	phone := "555-1234"
	key, value, ok := WhenNotNil("phone", &phone)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if key != "phone" {
		t.Errorf("expected key=phone, got %s", key)
	}
	if value.(*string) != &phone {
		t.Errorf("expected value to be the phone pointer")
	}
}

func TestWhenNotNil_Nil(t *testing.T) {
	_, _, ok := WhenNotNil("phone", nil)
	if ok {
		t.Fatal("expected ok=false for nil value")
	}
}

func TestWhenFunc_True(t *testing.T) {
	called := false
	key, value, ok := WhenFunc(true, "computed", func() any {
		called = true
		return 42
	})
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !called {
		t.Fatal("expected fn to be called")
	}
	if key != "computed" || value != 42 {
		t.Errorf("expected computed=42, got %s=%v", key, value)
	}
}

func TestWhenFunc_False(t *testing.T) {
	called := false
	_, _, ok := WhenFunc(false, "computed", func() any {
		called = true
		return 42
	})
	if ok {
		t.Fatal("expected ok=false")
	}
	if called {
		t.Fatal("expected fn NOT to be called when condition is false")
	}
}

func TestMerge(t *testing.T) {
	base := map[string]any{
		"id":   1,
		"name": "Alice",
	}

	Merge(base,
		func(m map[string]any) {
			if k, v, ok := When(true, "role", "admin"); ok {
				m[k] = v
			}
		},
		func(m map[string]any) {
			if k, v, ok := When(false, "secret", "hidden"); ok {
				m[k] = v
			}
		},
	)

	if base["role"] != "admin" {
		t.Errorf("expected role=admin after merge, got %v", base["role"])
	}
	if _, exists := base["secret"]; exists {
		t.Error("expected secret to not be merged (condition was false)")
	}
	// Original keys preserved
	if base["id"] != 1 || base["name"] != "Alice" {
		t.Error("expected original keys to be preserved")
	}
}

func TestNewCollection(t *testing.T) {
	users := []testUser{
		{ID: 1, Name: "Alice", Email: "alice@example.com"},
		{ID: 2, Name: "Bob", Email: "bob@example.com"},
	}

	result := NewCollection(users)
	if len(result) != 2 {
		t.Fatalf("expected 2 items, got %d", len(result))
	}
	if result[0]["name"] != "Alice" {
		t.Errorf("expected first item name=Alice, got %v", result[0]["name"])
	}
	if result[1]["name"] != "Bob" {
		t.Errorf("expected second item name=Bob, got %v", result[1]["name"])
	}
}

func TestNewCollection_Empty(t *testing.T) {
	result := NewCollection([]testUser{})
	if len(result) != 0 {
		t.Fatalf("expected 0 items, got %d", len(result))
	}
}

func TestNewPaginatedCollection(t *testing.T) {
	users := []testUser{
		{ID: 1, Name: "Alice", Email: "alice@example.com"},
		{ID: 2, Name: "Bob", Email: "bob@example.com"},
	}

	result := NewPaginatedCollection(users, PaginationMeta{
		Total:       25,
		PerPage:     10,
		CurrentPage: 1,
	})

	data, ok := result["data"].([]map[string]any)
	if !ok {
		t.Fatal("expected data to be []map[string]any")
	}
	if len(data) != 2 {
		t.Fatalf("expected 2 data items, got %d", len(data))
	}

	meta, ok := result["meta"].(PaginationMeta)
	if !ok {
		t.Fatal("expected meta to be PaginationMeta")
	}
	if meta.Total != 25 {
		t.Errorf("expected total=25, got %d", meta.Total)
	}
	if meta.PerPage != 10 {
		t.Errorf("expected per_page=10, got %d", meta.PerPage)
	}
	if meta.CurrentPage != 1 {
		t.Errorf("expected current_page=1, got %d", meta.CurrentPage)
	}
	if meta.LastPage != 3 {
		t.Errorf("expected auto-computed last_page=3, got %d", meta.LastPage)
	}
}

func TestNewPaginatedCollection_ExactDivision(t *testing.T) {
	result := NewPaginatedCollection([]testUser{}, PaginationMeta{
		Total:       20,
		PerPage:     10,
		CurrentPage: 1,
	})

	meta := result["meta"].(PaginationMeta)
	if meta.LastPage != 2 {
		t.Errorf("expected last_page=2 for exact division, got %d", meta.LastPage)
	}
}

func TestNewPaginatedCollection_ZeroPerPage(t *testing.T) {
	result := NewPaginatedCollection([]testUser{}, PaginationMeta{
		Total:       10,
		PerPage:     0,
		CurrentPage: 1,
	})

	meta := result["meta"].(PaginationMeta)
	if meta.LastPage != 0 {
		t.Errorf("expected last_page=0 for zero per_page, got %d", meta.LastPage)
	}
}

func TestNewPaginatedCollection_ZeroTotal(t *testing.T) {
	result := NewPaginatedCollection([]testUser{}, PaginationMeta{
		Total:       0,
		PerPage:     10,
		CurrentPage: 1,
	})

	meta := result["meta"].(PaginationMeta)
	if meta.LastPage != 0 {
		t.Errorf("expected last_page=0 for zero total, got %d", meta.LastPage)
	}
}

func TestFromPaginator(t *testing.T) {
	p := testPaginator{
		total:       50,
		perPage:     15,
		currentPage: 2,
		items:       nil,
	}

	meta := FromPaginator(p)
	if meta.Total != 50 {
		t.Errorf("expected total=50, got %d", meta.Total)
	}
	if meta.PerPage != 15 {
		t.Errorf("expected per_page=15, got %d", meta.PerPage)
	}
	if meta.CurrentPage != 2 {
		t.Errorf("expected current_page=2, got %d", meta.CurrentPage)
	}
	if meta.LastPage != 4 {
		t.Errorf("expected last_page=4 (ceil(50/15)), got %d", meta.LastPage)
	}
}

func TestFromPaginator_ZeroPerPage(t *testing.T) {
	p := testPaginator{total: 10, perPage: 0, currentPage: 1}
	meta := FromPaginator(p)
	if meta.LastPage != 0 {
		t.Errorf("expected last_page=0 for zero per_page, got %d", meta.LastPage)
	}
}

func TestResourceWithConditionals(t *testing.T) {
	phone := "555-9999"

	tests := []struct {
		name      string
		user      testUserWithConditionals
		wantRole  bool
		wantPhone bool
	}{
		{
			name:      "admin with phone",
			user:      testUserWithConditionals{ID: 1, Name: "Alice", IsAdmin: true, Phone: &phone},
			wantRole:  true,
			wantPhone: true,
		},
		{
			name:      "non-admin without phone",
			user:      testUserWithConditionals{ID: 2, Name: "Bob", IsAdmin: false, Phone: nil},
			wantRole:  false,
			wantPhone: false,
		},
		{
			name:      "admin without phone",
			user:      testUserWithConditionals{ID: 3, Name: "Charlie", IsAdmin: true, Phone: nil},
			wantRole:  true,
			wantPhone: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.user.ToResource()

			_, hasRole := result["role"]
			if hasRole != tt.wantRole {
				t.Errorf("role presence: got %v, want %v", hasRole, tt.wantRole)
			}
			_, hasPhone := result["phone"]
			if hasPhone != tt.wantPhone {
				t.Errorf("phone presence: got %v, want %v", hasPhone, tt.wantPhone)
			}
		})
	}
}

func TestWhenNotNil_NilValues(t *testing.T) {
	// Test with various nil-like values
	_, _, ok := WhenNotNil("key", nil)
	if ok {
		t.Error("expected ok=false for nil")
	}

	// Non-nil value should be included
	_, _, ok = WhenNotNil("key", 0)
	if !ok {
		t.Error("expected ok=true for zero int (not nil)")
	}

	_, _, ok = WhenNotNil("key", "")
	if !ok {
		t.Error("expected ok=true for empty string (not nil)")
	}

	_, _, ok = WhenNotNil("key", false)
	if !ok {
		t.Error("expected ok=true for false (not nil)")
	}
}
