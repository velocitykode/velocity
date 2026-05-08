package orm

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// ScopeUser is a non-soft-delete model used to exercise generic global
// scopes without entanglement with the auto-installed soft-delete scope.
type ScopeUser struct {
	Model[ScopeUser]
	Name     string `orm:"column:name"`
	TenantID int    `orm:"column:tenant_id"`
	Active   bool   `orm:"column:active"`
}

// TableName returns the table name used by ScopeUser.
func (ScopeUser) TableName() string {
	return "scope_users"
}

// setupScopeTests builds an in-memory sqlite database with a
// scope_users table seeded with a small fixture, sets it as Default,
// and clears any previously-registered global scopes for ScopeUser.
func setupScopeTests(t *testing.T) *Manager {
	t.Helper()
	manager := newTestManager(t)
	if _, err := manager.DB().Exec(`CREATE TABLE scope_users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		tenant_id INTEGER NOT NULL DEFAULT 0,
		active BOOLEAN NOT NULL DEFAULT 1,
		created_at DATETIME,
		updated_at DATETIME
	)`); err != nil {
		t.Fatalf("create scope_users: %v", err)
	}
	if _, err := manager.DB().Exec(`INSERT INTO scope_users (name, tenant_id, active, created_at, updated_at) VALUES
		('alice', 1, 1, datetime('now'), datetime('now')),
		('bob',   1, 0, datetime('now'), datetime('now')),
		('carol', 2, 1, datetime('now'), datetime('now')),
		('dave',  2, 0, datetime('now'), datetime('now'))`); err != nil {
		t.Fatalf("seed scope_users: %v", err)
	}
	SetDefault(manager)
	t.Cleanup(func() {
		clearGlobalScopes[ScopeUser]()
		ResetDefault()
		manager.Shutdown(context.Background())
	})
	clearGlobalScopes[ScopeUser]()
	return manager
}

// clearGlobalScopes removes every registered global scope for type T.
// Used by tests to reset registry state between subtests.
func clearGlobalScopes[T any]() {
	t := modelTypeFor[T]()
	if t == nil {
		return
	}
	reg := scopeRegistryFor(t)
	reg.mu.Lock()
	reg.entries = make(map[string]*scopeEntry)
	reg.next = 0
	reg.mu.Unlock()
}

// TestAddGlobalScope confirms a registered scope is applied to every
// query produced by Model[T] terminals.
func TestAddGlobalScope(t *testing.T) {
	setupScopeTests(t)

	AddGlobalScope[ScopeUser]("tenant", func(_ context.Context, q *Query[ScopeUser]) {
		q.Where("tenant_id = ?", 1)
	})

	users, err := Model[ScopeUser]{}.All(context.Background())
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("got %d users, want 2 (tenant scope should hide tenant 2)", len(users))
	}
	for _, u := range users {
		if u.TenantID != 1 {
			t.Errorf("returned user from tenant %d; scope did not filter", u.TenantID)
		}
	}
}

// TestWithoutGlobalScope confirms a single named scope can be opted
// out by name while other scopes remain in effect.
func TestWithoutGlobalScope(t *testing.T) {
	setupScopeTests(t)

	AddGlobalScope[ScopeUser]("tenant", func(_ context.Context, q *Query[ScopeUser]) {
		q.Where("tenant_id = ?", 1)
	})
	AddGlobalScope[ScopeUser]("active", func(_ context.Context, q *Query[ScopeUser]) {
		q.Where("active = ?", true)
	})

	// With both scopes active, only alice (tenant 1, active) is visible.
	users, err := Model[ScopeUser]{}.All(context.Background())
	if err != nil {
		t.Fatalf("baseline All: %v", err)
	}
	if len(users) != 1 || users[0].Name != "alice" {
		t.Fatalf("baseline: got %d users (%v), want 1 (alice)", len(users), namesOf(users))
	}

	// Opt out of "tenant" only: active scope still applies, both
	// active users (alice + carol) come back.
	relaxed, err := newQuery[ScopeUser]().WithoutGlobalScope("tenant").Get(context.Background())
	if err != nil {
		t.Fatalf("relaxed Get: %v", err)
	}
	if len(relaxed) != 2 {
		t.Fatalf("got %d users, want 2 (active scope only)", len(relaxed))
	}
	for _, u := range relaxed {
		if !u.Active {
			t.Errorf("relaxed query returned inactive user %q; active scope should still apply", u.Name)
		}
	}
}

// TestWithoutGlobalScopes confirms WithoutGlobalScopes drops every
// registered scope for the type.
func TestWithoutGlobalScopes(t *testing.T) {
	setupScopeTests(t)

	AddGlobalScope[ScopeUser]("tenant", func(_ context.Context, q *Query[ScopeUser]) {
		q.Where("tenant_id = ?", 1)
	})
	AddGlobalScope[ScopeUser]("active", func(_ context.Context, q *Query[ScopeUser]) {
		q.Where("active = ?", true)
	})

	all, err := newQuery[ScopeUser]().WithoutGlobalScopes().Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(all) != 4 {
		t.Errorf("got %d users, want 4 (all scopes disabled)", len(all))
	}
}

// TestAddGlobalScope_Compose confirms multiple scopes intersect (AND
// composition) and registration order is preserved.
func TestAddGlobalScope_Compose(t *testing.T) {
	setupScopeTests(t)

	var order []string
	AddGlobalScope[ScopeUser]("first", func(_ context.Context, q *Query[ScopeUser]) {
		order = append(order, "first")
		q.Where("tenant_id = ?", 1)
	})
	AddGlobalScope[ScopeUser]("second", func(_ context.Context, q *Query[ScopeUser]) {
		order = append(order, "second")
		q.Where("active = ?", true)
	})

	users, err := Model[ScopeUser]{}.All(context.Background())
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(users) != 1 || users[0].Name != "alice" {
		t.Errorf("composed scopes returned %v, want [alice]", namesOf(users))
	}
	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Errorf("scope apply order = %v, want [first second]", order)
	}
}

// tenantKey is the context key used by the FromContext scope test.
type tenantKey struct{}

// TestAddGlobalScope_FromContext confirms a scope can read its
// predicate value from the per-call ctx forwarded by applyGlobalScopes,
// the consumer's chosen mechanism for plumbing tenant / actor / locale
// data. The ctx is the same context.Context the caller passed to the
// terminal that triggered the scope.
func TestAddGlobalScope_FromContext(t *testing.T) {
	setupScopeTests(t)

	AddGlobalScope[ScopeUser]("tenant_from_ctx", func(ctx context.Context, q *Query[ScopeUser]) {
		if v, ok := ctx.Value(tenantKey{}).(int); ok {
			q.Where("tenant_id = ?", v)
		}
	})

	// Tenant 2: should yield carol + dave (regardless of active).
	ctx := context.WithValue(context.Background(), tenantKey{}, 2)
	users, err := newQuery[ScopeUser]().Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("tenant=2 Get got %d users, want 2 (%v)", len(users), namesOf(users))
	}
	for _, u := range users {
		if u.TenantID != 2 {
			t.Errorf("tenant scope returned user from tenant %d", u.TenantID)
		}
	}

	// Without the ctx value, the scope is a no-op and all rows return.
	all, err := newQuery[ScopeUser]().Get(context.Background())
	if err != nil {
		t.Fatalf("no-ctx Get: %v", err)
	}
	if len(all) != 4 {
		t.Errorf("no-ctx Get got %d users, want 4", len(all))
	}
}

// TestAddGlobalScope_LeakRegression is the security-class assertion:
// a registered tenant scope cannot be silently bypassed by code that
// merely forgets to call .Where("tenant_id = ?", ...). The scope must
// fire on every Model[T] query path.
func TestAddGlobalScope_LeakRegression(t *testing.T) {
	setupScopeTests(t)

	const callerTenant = 1
	AddGlobalScope[ScopeUser]("tenant", func(_ context.Context, q *Query[ScopeUser]) {
		q.Where("tenant_id = ?", callerTenant)
	})

	// Caller forgot to scope explicitly; framework must enforce.
	usersAll, err := Model[ScopeUser]{}.All(context.Background())
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	for _, u := range usersAll {
		if u.TenantID != callerTenant {
			t.Fatalf("CROSS-TENANT LEAK: All() returned user %q from tenant %d (caller is %d)",
				u.Name, u.TenantID, callerTenant)
		}
	}

	// Even Count, Pluck, and Where must enforce.
	count, err := Model[ScopeUser]{}.Count(context.Background())
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 2 {
		t.Errorf("Count = %d, want 2 (scope must enforce)", count)
	}

	names, err := Model[ScopeUser]{}.Pluck(context.Background(), "name")
	if err != nil {
		t.Fatalf("Pluck: %v", err)
	}
	if len(names) != 2 {
		t.Errorf("Pluck returned %d names, want 2 (scope must enforce)", len(names))
	}

	// Caller's own Where must AND with the scope, not replace it.
	mixed, err := Model[ScopeUser]{}.Where("active = ?", true).Get(context.Background())
	if err != nil {
		t.Fatalf("Where Get: %v", err)
	}
	for _, u := range mixed {
		if u.TenantID != callerTenant {
			t.Fatalf("CROSS-TENANT LEAK via Where: user %q from tenant %d", u.Name, u.TenantID)
		}
	}

	// Update must respect the scope: an unfiltered Update must not
	// touch other tenants' rows.
	affected, err := newQuery[ScopeUser]().Update(context.Background(), map[string]any{"name": "renamed"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if affected != 2 {
		t.Errorf("Update affected = %d, want 2 (scope must enforce)", affected)
	}
	// Confirm tenant 2 names are intact.
	other, err := newQuery[ScopeUser]().WithoutGlobalScopes().Where("tenant_id = ?", 2).Get(context.Background())
	if err != nil {
		t.Fatalf("verify tenant 2: %v", err)
	}
	for _, u := range other {
		if u.Name == "renamed" {
			t.Fatalf("CROSS-TENANT WRITE LEAK: tenant-2 user %d was renamed", u.ID)
		}
	}
}

// TestAddGlobalScope_Concurrent stresses the registry and apply path
// under N goroutines. Run with -race; a failure will surface as a
// data-race report or as wrong row counts.
func TestAddGlobalScope_Concurrent(t *testing.T) {
	// In-memory sqlite gives each connection its own database, so
	// pin the pool to a single connection for this test (same pattern
	// the drivers/context_test.go cases use).
	manager, err := NewManager(ManagerConfig{
		Driver: "sqlite", Database: ":memory:", MaxOpenConns: 1,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if _, err := manager.DB().Exec(`CREATE TABLE scope_users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		tenant_id INTEGER NOT NULL DEFAULT 0,
		active BOOLEAN NOT NULL DEFAULT 1,
		created_at DATETIME,
		updated_at DATETIME
	)`); err != nil {
		t.Fatalf("create scope_users: %v", err)
	}
	if _, err := manager.DB().Exec(`INSERT INTO scope_users (name, tenant_id, active, created_at, updated_at) VALUES
		('alice', 1, 1, datetime('now'), datetime('now')),
		('bob',   1, 0, datetime('now'), datetime('now')),
		('carol', 2, 1, datetime('now'), datetime('now')),
		('dave',  2, 0, datetime('now'), datetime('now'))`); err != nil {
		t.Fatalf("seed scope_users: %v", err)
	}
	SetDefault(manager)
	t.Cleanup(func() {
		clearGlobalScopes[ScopeUser]()
		ResetDefault()
		manager.Shutdown(context.Background())
	})
	clearGlobalScopes[ScopeUser]()

	// Pre-register one scope so every concurrent query has work to do.
	AddGlobalScope[ScopeUser]("tenant", func(_ context.Context, q *Query[ScopeUser]) {
		q.Where("tenant_id = ?", 1)
	})

	const goroutines = 32
	const iterations = 25

	var wg sync.WaitGroup
	var failures atomic.Int32

	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				// Each goroutine alternates between adding a scope
				// (mutating the registry) and querying (reading it).
				name := fmt.Sprintf("ephemeral_%d", id)
				if i%5 == 0 {
					AddGlobalScope[ScopeUser](name, func(_ context.Context, q *Query[ScopeUser]) {
						q.Where("active = ?", true)
					})
				}
				if i%7 == 0 {
					RemoveGlobalScope[ScopeUser](name)
				}
				users, err := Model[ScopeUser]{}.All(context.Background())
				if err != nil {
					failures.Add(1)
					return
				}
				for _, u := range users {
					if u.TenantID != 1 {
						failures.Add(1)
						return
					}
				}
			}
		}(g)
	}
	wg.Wait()

	if failures.Load() != 0 {
		t.Errorf("concurrent run hit %d failures (cross-tenant leak or query error)", failures.Load())
	}
}

// TestSoftDelete_IsRegisteredAsGlobalScope verifies the existing
// soft-delete behaviour is now driven by the new global-scope primitive
// (auto-installed under the SoftDeleteScopeName name).
func TestSoftDelete_IsRegisteredAsGlobalScope(t *testing.T) {
	m := setupRawScopeTest(t)
	SetDefault(m)
	t.Cleanup(func() { ResetDefault() })

	// Trigger newQuery so the soft-delete scope auto-registers.
	_ = newQuery[RawScopeUser]()

	// Default-scoped: trashed row hidden.
	users, err := Model[RawScopeUser]{}.All(context.Background())
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(users) != 1 || users[0].Name != "alive" {
		t.Errorf("default scope returned %v, want [alive]", users)
	}

	// Opt-out by name: trashed row visible again.
	all, err := newQuery[RawScopeUser]().WithoutGlobalScope(SoftDeleteScopeName).Get(context.Background())
	if err != nil {
		t.Fatalf("WithoutGlobalScope Get: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("WithoutGlobalScope(SoftDeleteScopeName) returned %d rows, want 2", len(all))
	}
}

// TestClone_PropagatesScopeDisableState confirms WithoutGlobalScope
// state is carried by Clone so forked queries do not silently
// re-enable a scope the parent disabled.
func TestClone_PropagatesScopeDisableState(t *testing.T) {
	setupScopeTests(t)

	AddGlobalScope[ScopeUser]("tenant", func(_ context.Context, q *Query[ScopeUser]) {
		q.Where("tenant_id = ?", 1)
	})

	parent := newQuery[ScopeUser]().WithoutGlobalScope("tenant")
	child := parent.Clone()
	if !child.disabledScopes["tenant"] {
		t.Fatal("Clone did not propagate disabledScopes")
	}

	// Mutating the child's disabledScopes must not affect the parent.
	child.disabledScopes["other"] = true
	if parent.disabledScopes["other"] {
		t.Error("Clone aliased disabledScopes; parent saw child's mutation")
	}
}

// namesOf is a small test helper for readable failure messages.
func namesOf(users []ScopeUser) []string {
	out := make([]string, len(users))
	for i, u := range users {
		out[i] = u.Name
	}
	return out
}
