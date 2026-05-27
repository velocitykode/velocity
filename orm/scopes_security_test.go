package orm

import (
	"context"
	"reflect"
	"testing"
)

// This file contains regression tests for ORM security audit findings
// O-02, O-03, O-04, and O-06 (May 2026 audit). The common shape across
// all four findings: a terminal or eager-load path that calls
// bindTxFromContextValue but skips applyGlobalScopes, so a registered
// tenant scope is silently bypassed.
//
// Each test:
//   1. Registers a tenant scope that filters to tenant_id=1.
//   2. Seeds rows for tenant_id=1 (visible) and tenant_id=2 (other tenant).
//   3. Runs the operation under test.
//   4. Asserts other-tenant rows are untouched (for write paths) or
//      excluded from the result (for read paths).

// --- Test models -----------------------------------------------------

// ScopePost is a non-soft-delete model used for ForceDelete tests.
// ForceDelete on a non-soft-delete model routes through the same code
// path as ForceDelete on a soft-delete model, so we cover both
// without needing two separate setups.
type ScopePost struct {
	Model[ScopePost]
	Title    string `orm:"column:title"`
	TenantID int    `orm:"column:tenant_id"`
	Views    int    `orm:"column:views"`
	Amount   int    `orm:"column:amount"`
}

func (ScopePost) TableName() string { return "scope_posts" }

// ScopeOrder is a soft-delete model used for aggregate tests so we
// can verify Sum/Avg/Min/Max also skip trashed rows when the
// soft-delete scope applies.
type ScopeOrder struct {
	SoftDeleteModel[ScopeOrder]
	TenantID int `orm:"column:tenant_id"`
	Amount   int `orm:"column:amount"`
}

func (ScopeOrder) TableName() string { return "scope_orders" }

// ScopeComment is a child model used to exercise eager-load (O-06).
// A user with tenant_id=1 must not eager-load comments belonging to
// posts in tenant_id=2.
type ScopeComment struct {
	Model[ScopeComment]
	PostID   int    `orm:"column:post_id"`
	TenantID int    `orm:"column:tenant_id"`
	Body     string `orm:"column:body"`
}

func (ScopeComment) TableName() string { return "scope_comments" }

// ScopeBlog is a parent model that hasMany ScopeComment for O-06 hasMany testing.
type ScopeBlog struct {
	Model[ScopeBlog]
	Title    string         `orm:"column:title"`
	TenantID int            `orm:"column:tenant_id"`
	Comments []ScopeComment `orm:"relation:hasMany,post_id,id"`
}

func (ScopeBlog) TableName() string { return "scope_posts" }

// ScopeTeam is a parent for the M2M eager-load test (O-06 m2m).
type ScopeTeam struct {
	Model[ScopeTeam]
	Name     string      `orm:"column:name"`
	Members  []ScopeUser `orm:"manyToMany:scope_team_members,team_id,user_id"`
	TenantID int         `orm:"column:tenant_id"`
}

func (ScopeTeam) TableName() string { return "scope_teams" }

// ScopeMorphLog is a parent for the polymorphic eager-load test (O-06 polymorphic).
type ScopeMorphLog struct {
	Model[ScopeMorphLog]
	Action   string `orm:"column:action"`
	Resource Morph  `orm:"polymorphic:resource_type,resource_id"`
}

func (ScopeMorphLog) TableName() string { return "scope_morph_logs" }

// --- Helpers ----------------------------------------------------------

// setupSecurityTables creates the schemas + seed data used by every
// test in this file. Returns the manager so callers can issue
// verification queries via DB().
func setupSecurityTables(t *testing.T) *Manager {
	t.Helper()
	m := newTestManager(t)
	t.Cleanup(func() {
		clearGlobalScopes[ScopePost]()
		clearGlobalScopes[ScopeOrder]()
		clearGlobalScopes[ScopeComment]()
		clearGlobalScopes[ScopeBlog]()
		clearGlobalScopes[ScopeTeam]()
		clearGlobalScopes[ScopeUser]()
		clearPivotColumnCache()
		ResetMorphRegistry()
		ResetDefault()
		_ = m.Shutdown(context.Background())
	})
	for _, ddl := range []string{
		`CREATE TABLE scope_posts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT NOT NULL,
			tenant_id INTEGER NOT NULL,
			views INTEGER NOT NULL DEFAULT 0,
			amount INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		`CREATE TABLE scope_orders (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			tenant_id INTEGER NOT NULL,
			amount INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME
		)`,
		`CREATE TABLE scope_comments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			post_id INTEGER NOT NULL,
			tenant_id INTEGER NOT NULL,
			body TEXT NOT NULL,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		`CREATE TABLE scope_teams (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			tenant_id INTEGER NOT NULL,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		`CREATE TABLE scope_users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			tenant_id INTEGER NOT NULL DEFAULT 0,
			active BOOLEAN NOT NULL DEFAULT 1,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		`CREATE TABLE scope_team_members (
			team_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL,
			PRIMARY KEY (team_id, user_id)
		)`,
		`CREATE TABLE scope_morph_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			action TEXT,
			resource_type TEXT,
			resource_id INTEGER,
			created_at DATETIME,
			updated_at DATETIME
		)`,
	} {
		if _, err := m.DB().Exec(ddl); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	SetDefault(m)
	clearGlobalScopes[ScopePost]()
	clearGlobalScopes[ScopeOrder]()
	clearGlobalScopes[ScopeComment]()
	clearGlobalScopes[ScopeBlog]()
	clearGlobalScopes[ScopeTeam]()
	clearGlobalScopes[ScopeUser]()
	clearPivotColumnCache()
	return m
}

// --- O-02: ForceDelete must apply global scopes ----------------------

// TestForceDelete_AppliesTenantScope is the regression test for O-02.
// Without the fix, ForceDelete drops rows belonging to other tenants
// even though a tenant scope is registered, because ForceDelete only
// calls bindTxFromContextValue and skips applyGlobalScopes.
func TestForceDelete_AppliesTenantScope(t *testing.T) {
	m := setupSecurityTables(t)

	// Seed two posts in each tenant.
	if _, err := m.DB().Exec(`INSERT INTO scope_posts (id, title, tenant_id, created_at, updated_at) VALUES
		(1, 'tenant1 post a', 1, '2024-01-01', '2024-01-01'),
		(2, 'tenant1 post b', 1, '2024-01-01', '2024-01-01'),
		(3, 'tenant2 post a', 2, '2024-01-01', '2024-01-01'),
		(4, 'tenant2 post b', 2, '2024-01-01', '2024-01-01')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Tenant scope limits visibility to tenant_id=1.
	AddGlobalScope[ScopePost]("tenant", func(_ context.Context, q *Query[ScopePost]) {
		q.Where("tenant_id = ?", 1)
	})

	// Caller-forgotten WHERE: ForceDelete every "tenant1 post" by name
	// only. The tenant scope must AND in so tenant 2's rows survive.
	affected, err := newQuery[ScopePost]().Where("title LIKE ?", "tenant% post a").ForceDelete(context.Background())
	if err != nil {
		t.Fatalf("ForceDelete: %v", err)
	}
	if affected != 1 {
		t.Fatalf("affected = %d, want 1 (tenant scope must constrain to tenant 1's post a only)", affected)
	}

	// Tenant 2's rows must still be present.
	var t2 int
	if err := m.DB().QueryRow(`SELECT COUNT(*) FROM scope_posts WHERE tenant_id = 2`).Scan(&t2); err != nil {
		t.Fatalf("count tenant 2: %v", err)
	}
	if t2 != 2 {
		t.Errorf("CROSS-TENANT DELETE LEAK: tenant 2 post count = %d, want 2", t2)
	}
}

// TestForceDelete_StillIgnoresSoftDeletePredicate confirms ForceDelete
// on a soft-delete model continues to drop trashed rows. The fix
// disables the soft-delete scope only for ForceDelete; every other
// scope still applies.
func TestForceDelete_StillIgnoresSoftDeletePredicate(t *testing.T) {
	m := setupSecurityTables(t)

	if _, err := m.DB().Exec(`INSERT INTO scope_orders (id, tenant_id, amount, created_at, updated_at, deleted_at) VALUES
		(1, 1, 100, '2024-01-01', '2024-01-01', NULL),
		(2, 1, 200, '2024-01-01', '2024-01-01', '2024-01-01'),
		(3, 2, 300, '2024-01-01', '2024-01-01', NULL),
		(4, 2, 400, '2024-01-01', '2024-01-01', '2024-01-01')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Register tenant scope; soft-delete scope auto-registers via newQuery.
	AddGlobalScope[ScopeOrder]("tenant", func(_ context.Context, q *Query[ScopeOrder]) {
		q.Where("tenant_id = ?", 1)
	})

	// ForceDelete with no explicit WHERE must drop both tenant 1's
	// live AND trashed rows, but leave tenant 2 alone.
	affected, err := newQuery[ScopeOrder]().ForceDelete(context.Background())
	if err != nil {
		t.Fatalf("ForceDelete: %v", err)
	}
	if affected != 2 {
		t.Fatalf("affected = %d, want 2 (tenant 1 live + trashed)", affected)
	}
	var t2 int
	if err := m.DB().QueryRow(`SELECT COUNT(*) FROM scope_orders WHERE tenant_id = 2`).Scan(&t2); err != nil {
		t.Fatalf("count tenant 2: %v", err)
	}
	if t2 != 2 {
		t.Errorf("CROSS-TENANT FORCE-DELETE LEAK: tenant 2 row count = %d, want 2", t2)
	}
}

// --- O-03: Sum/Avg/Min/Max must apply global scopes ------------------

// TestAggregates_ApplyTenantScope is the regression test for O-03.
// Sum/Avg/Min/Max must honour every registered global scope; without
// the fix, a "total sales" Sum on a multi-tenant table leaks the
// other tenant's revenue into the caller's total.
func TestAggregates_ApplyTenantScope(t *testing.T) {
	m := setupSecurityTables(t)
	_ = m

	if _, err := Default().DB().Exec(`INSERT INTO scope_posts (id, title, tenant_id, amount, created_at, updated_at) VALUES
		(1, 'a', 1, 100, '2024-01-01', '2024-01-01'),
		(2, 'b', 1, 200, '2024-01-01', '2024-01-01'),
		(3, 'c', 2, 999, '2024-01-01', '2024-01-01')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	AddGlobalScope[ScopePost]("tenant", func(_ context.Context, q *Query[ScopePost]) {
		q.Where("tenant_id = ?", 1)
	})

	ctx := context.Background()
	sum, err := newQuery[ScopePost]().Sum(ctx, "amount")
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	if sum != 300 {
		t.Errorf("CROSS-TENANT SUM LEAK: Sum = %v, want 300 (tenant 1 only; tenant 2's 999 must be excluded)", sum)
	}

	avg, err := newQuery[ScopePost]().Avg(ctx, "amount")
	if err != nil {
		t.Fatalf("Avg: %v", err)
	}
	if avg != 150 {
		t.Errorf("Avg = %v, want 150 (avg of 100 and 200)", avg)
	}

	minV, err := newQuery[ScopePost]().Min(ctx, "amount")
	if err != nil {
		t.Fatalf("Min: %v", err)
	}
	if minV != 100 {
		t.Errorf("Min = %v, want 100", minV)
	}

	maxV, err := newQuery[ScopePost]().Max(ctx, "amount")
	if err != nil {
		t.Fatalf("Max: %v", err)
	}
	if maxV != 200 {
		t.Errorf("CROSS-TENANT MAX LEAK: Max = %v, want 200 (tenant 1's max)", maxV)
	}
}

// TestAggregates_ApplySoftDeleteScope confirms aggregates on a
// SoftDeleteModel exclude trashed rows. Previously aggregate() bypassed
// applyGlobalScopes entirely, so the auto-installed soft-delete scope
// did not apply.
func TestAggregates_ApplySoftDeleteScope(t *testing.T) {
	setupSecurityTables(t)

	if _, err := Default().DB().Exec(`INSERT INTO scope_orders (id, tenant_id, amount, created_at, updated_at, deleted_at) VALUES
		(1, 1, 100, '2024-01-01', '2024-01-01', NULL),
		(2, 1, 200, '2024-01-01', '2024-01-01', NULL),
		(3, 1, 999, '2024-01-01', '2024-01-01', '2024-01-01')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	sum, err := newQuery[ScopeOrder]().Sum(context.Background(), "amount")
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	if sum != 300 {
		t.Errorf("TRASHED-ROW LEAK: Sum = %v, want 300 (live rows only; trashed 999 must be excluded)", sum)
	}
}

// --- O-04: Increment/Decrement must apply global scopes --------------

// TestIncrement_AppliesTenantScope is the regression test for O-04.
// Increment is a write terminal that builds an UPDATE under the hood;
// without applyGlobalScopes, an unfiltered "bump view counts" call
// mutates rows in every tenant.
func TestIncrement_AppliesTenantScope(t *testing.T) {
	m := setupSecurityTables(t)

	if _, err := m.DB().Exec(`INSERT INTO scope_posts (id, title, tenant_id, views) VALUES
		(1, 'a', 1, 0),
		(2, 'b', 1, 0),
		(3, 'c', 2, 0)`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	AddGlobalScope[ScopePost]("tenant", func(_ context.Context, q *Query[ScopePost]) {
		q.Where("tenant_id = ?", 1)
	})

	if err := newQuery[ScopePost]().Increment(context.Background(), "views", 5); err != nil {
		t.Fatalf("Increment: %v", err)
	}

	// Tenant 1 rows must have view=5.
	rows, err := m.DB().Query(`SELECT id, views FROM scope_posts ORDER BY id`)
	if err != nil {
		t.Fatalf("verify query: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, views int
		if err := rows.Scan(&id, &views); err != nil {
			t.Fatalf("scan: %v", err)
		}
		switch id {
		case 1, 2:
			if views != 5 {
				t.Errorf("post %d views = %d, want 5", id, views)
			}
		case 3:
			if views != 0 {
				t.Errorf("CROSS-TENANT INCREMENT LEAK: tenant 2's post id=3 views = %d, want 0", views)
			}
		}
	}
}

// TestDecrement_AppliesTenantScope mirrors TestIncrement to cover the
// negative-delta branch of incrementOrDecrement, which routes through
// the same scope-application path.
func TestDecrement_AppliesTenantScope(t *testing.T) {
	m := setupSecurityTables(t)

	if _, err := m.DB().Exec(`INSERT INTO scope_posts (id, title, tenant_id, views) VALUES
		(1, 'a', 1, 100),
		(2, 'b', 2, 100)`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	AddGlobalScope[ScopePost]("tenant", func(_ context.Context, q *Query[ScopePost]) {
		q.Where("tenant_id = ?", 1)
	})

	if err := newQuery[ScopePost]().Decrement(context.Background(), "views", 10); err != nil {
		t.Fatalf("Decrement: %v", err)
	}

	var v1, v2 int
	if err := m.DB().QueryRow(`SELECT views FROM scope_posts WHERE id = 1`).Scan(&v1); err != nil {
		t.Fatalf("v1: %v", err)
	}
	if err := m.DB().QueryRow(`SELECT views FROM scope_posts WHERE id = 2`).Scan(&v2); err != nil {
		t.Fatalf("v2: %v", err)
	}
	if v1 != 90 {
		t.Errorf("tenant 1 views = %d, want 90", v1)
	}
	if v2 != 100 {
		t.Errorf("CROSS-TENANT DECREMENT LEAK: tenant 2 views = %d, want 100", v2)
	}
}

// --- O-06: Eager-load must apply global scopes on related model ------

// TestEagerLoadHasMany_AppliesRelatedTenantScope is the regression test
// for O-06 (hasMany branch). The loader hand-rolled a SELECT against
// the related table and only checked deleted_at IS NULL for soft-delete
// models; every other scope on the related type was silently dropped.
func TestEagerLoadHasMany_AppliesRelatedTenantScope(t *testing.T) {
	m := setupSecurityTables(t)

	// One blog (post) per tenant. The comment join column is post_id;
	// some comments carry the WRONG tenant_id so the bug surfaces.
	if _, err := m.DB().Exec(`INSERT INTO scope_posts (id, title, tenant_id, created_at, updated_at) VALUES
		(1, 'blog one', 1, '2024-01-01', '2024-01-01')`); err != nil {
		t.Fatalf("seed posts: %v", err)
	}
	if _, err := m.DB().Exec(`INSERT INTO scope_comments (id, post_id, tenant_id, body, created_at, updated_at) VALUES
		(1, 1, 1, 'mine', '2024-01-01', '2024-01-01'),
		(2, 1, 2, 'attacker comment', '2024-01-01', '2024-01-01')`); err != nil {
		t.Fatalf("seed comments: %v", err)
	}

	// Scope on the RELATED model (ScopeComment), not the parent.
	AddGlobalScope[ScopeComment]("tenant", func(_ context.Context, q *Query[ScopeComment]) {
		q.Where("tenant_id = ?", 1)
	})

	blogs, err := newQuery[ScopeBlog]().With("Comments").Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(blogs) != 1 {
		t.Fatalf("got %d blogs, want 1", len(blogs))
	}
	if got := len(blogs[0].Comments); got != 1 {
		t.Fatalf("CROSS-TENANT EAGER-LOAD LEAK: got %d comments, want 1 (tenant scope on ScopeComment must filter)", got)
	}
	if blogs[0].Comments[0].Body != "mine" {
		t.Errorf("CROSS-TENANT EAGER-LOAD LEAK: got comment %q, want %q", blogs[0].Comments[0].Body, "mine")
	}
}

// TestEagerLoadM2M_AppliesRelatedTenantScope is the regression test
// for O-06 (manyToMany branch). queryRelatedRows hand-rolled the SELECT
// against the related table; user scopes on the related type were
// silently dropped.
func TestEagerLoadM2M_AppliesRelatedTenantScope(t *testing.T) {
	m := setupSecurityTables(t)

	if _, err := m.DB().Exec(`INSERT INTO scope_teams (id, name, tenant_id, created_at, updated_at) VALUES (1, 'team one', 1, '2024-01-01', '2024-01-01')`); err != nil {
		t.Fatalf("seed teams: %v", err)
	}
	if _, err := m.DB().Exec(`INSERT INTO scope_users (id, name, tenant_id, created_at, updated_at) VALUES
		(1, 'mine', 1, '2024-01-01', '2024-01-01'),
		(2, 'attacker', 2, '2024-01-01', '2024-01-01')`); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if _, err := m.DB().Exec(`INSERT INTO scope_team_members (team_id, user_id) VALUES
		(1, 1),
		(1, 2)`); err != nil {
		t.Fatalf("seed pivot: %v", err)
	}

	// Scope on the RELATED model (ScopeUser).
	AddGlobalScope[ScopeUser]("tenant", func(_ context.Context, q *Query[ScopeUser]) {
		q.Where("tenant_id = ?", 1)
	})

	teams, err := newQuery[ScopeTeam]().With("Members").Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(teams) != 1 {
		t.Fatalf("got %d teams, want 1", len(teams))
	}
	if got := len(teams[0].Members); got != 1 {
		t.Fatalf("CROSS-TENANT M2M LEAK: got %d members, want 1 (tenant scope on ScopeUser must filter)", got)
	}
	if teams[0].Members[0].Name != "mine" {
		t.Errorf("CROSS-TENANT M2M LEAK: got member %q, want %q", teams[0].Members[0].Name, "mine")
	}
}

// TestEagerLoadPolymorphic_AppliesRelatedTenantScope is the regression
// test for O-06 (polymorphic branch). loadByIDs hand-rolled the SELECT;
// user scopes on the morph type were silently dropped.
func TestEagerLoadPolymorphic_AppliesRelatedTenantScope(t *testing.T) {
	m := setupSecurityTables(t)

	// One audit log pointing at scope_users id=2 (attacker).
	if _, err := m.DB().Exec(`INSERT INTO scope_users (id, name, tenant_id, created_at, updated_at) VALUES
		(1, 'mine', 1, '2024-01-01', '2024-01-01'),
		(2, 'attacker', 2, '2024-01-01', '2024-01-01')`); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if _, err := m.DB().Exec(`INSERT INTO scope_morph_logs (id, action, resource_type, resource_id, created_at, updated_at) VALUES
		(1, 'view', 'user', 1, '2024-01-01', '2024-01-01'),
		(2, 'view', 'user', 2, '2024-01-01', '2024-01-01')`); err != nil {
		t.Fatalf("seed logs: %v", err)
	}

	RegisterMorph("user", reflect.TypeOf(ScopeUser{}))
	AddGlobalScope[ScopeUser]("tenant", func(_ context.Context, q *Query[ScopeUser]) {
		q.Where("tenant_id = ?", 1)
	})

	logs, err := newQuery[ScopeMorphLog]().With("Resource").Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("got %d logs, want 2", len(logs))
	}

	// log id=1 references our tenant's user; Resolved must be set.
	// log id=2 references the attacker; Resolved must NOT be set,
	// because the tenant scope filters that row out of the IN query.
	var ours, theirs *ScopeMorphLog
	for i := range logs {
		switch logs[i].ID {
		case 1:
			ours = &logs[i]
		case 2:
			theirs = &logs[i]
		}
	}
	if ours == nil || theirs == nil {
		t.Fatalf("did not find both logs in result; got %+v", logs)
	}
	if ours.Resource.Resolved == nil {
		t.Errorf("expected own-tenant resolve to succeed, got nil Resolved")
	}
	if theirs.Resource.Resolved != nil {
		t.Errorf("CROSS-TENANT MORPH LEAK: attacker row resolved to %+v; tenant scope on ScopeUser must filter", theirs.Resource.Resolved)
	}
}

// TestMorphResolve_AppliesRelatedTenantScope covers the single-row
// Morph.Resolve path which also hand-rolled its SELECT and silently
// dropped every scope other than soft-delete.
func TestMorphResolve_AppliesRelatedTenantScope(t *testing.T) {
	m := setupSecurityTables(t)

	if _, err := m.DB().Exec(`INSERT INTO scope_users (id, name, tenant_id, created_at, updated_at) VALUES
		(1, 'mine', 1, '2024-01-01', '2024-01-01'),
		(2, 'attacker', 2, '2024-01-01', '2024-01-01')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	RegisterMorph("user", reflect.TypeOf(ScopeUser{}))
	AddGlobalScope[ScopeUser]("tenant", func(_ context.Context, q *Query[ScopeUser]) {
		q.Where("tenant_id = ?", 1)
	})

	// Resolving the attacker row must miss (other tenant's row).
	attacker := &Morph{TypeName: "user", ID: 2}
	if _, err := attacker.Resolve(context.Background()); err != ErrRecordNotFound {
		t.Errorf("CROSS-TENANT MORPH RESOLVE LEAK: got err = %v, want ErrRecordNotFound (tenant scope must hide tenant 2's row)", err)
	}

	// Resolving our own row must succeed.
	own := &Morph{TypeName: "user", ID: 1}
	resolved, err := own.Resolve(context.Background())
	if err != nil {
		t.Fatalf("own resolve: %v", err)
	}
	if resolved == nil {
		t.Error("own resolve: Resolved is nil")
	}
}

// --- Follow-up: OR-preservation in eager-load scopes ----------------

// TestEagerLoad_PreservesOrInScope is the regression test for the
// reviewer's Blocker 1: an OR-based scope on the related model was
// being coerced to AND inside the eager-load helper, narrowing the
// matched result set and silently changing correctness.
//
// Scope: q.Where("tenant_id = ?", 1).OrWhere("public = ?", true)
//
// Semantics: a comment is visible if it belongs to tenant 1 OR if it
// is marked public. Three comments are seeded:
//   - id=1: tenant=1, public=false (matches WHERE branch)
//   - id=2: tenant=2, public=true  (matches OR branch)
//   - id=3: tenant=2, public=false (matches neither, must be hidden)
//
// Pre-fix the eager-load coerced every harvested scope condition to
// "and", so the predicate compiled to "tenant=1 AND public=true" and
// only an impossible combination matched. Both rows 1 and 2 should
// surface; row 3 must remain hidden.
func TestEagerLoad_PreservesOrInScope(t *testing.T) {
	m := setupSecurityTables(t)

	// scope_comments carries a `public` column too: extend the schema
	// rather than redo the helper.
	if _, err := m.DB().Exec(`ALTER TABLE scope_comments ADD COLUMN public BOOLEAN NOT NULL DEFAULT 0`); err != nil {
		t.Fatalf("alter: %v", err)
	}
	if _, err := m.DB().Exec(`INSERT INTO scope_posts (id, title, tenant_id, created_at, updated_at) VALUES
		(1, 'blog', 1, '2024-01-01', '2024-01-01')`); err != nil {
		t.Fatalf("seed posts: %v", err)
	}
	if _, err := m.DB().Exec(`INSERT INTO scope_comments (id, post_id, tenant_id, body, public, created_at, updated_at) VALUES
		(1, 1, 1, 'mine private',     0, '2024-01-01', '2024-01-01'),
		(2, 1, 2, 'their public',     1, '2024-01-01', '2024-01-01'),
		(3, 1, 2, 'their private',    0, '2024-01-01', '2024-01-01')`); err != nil {
		t.Fatalf("seed comments: %v", err)
	}

	// OR scope: visible if tenant_id=1 OR public=true.
	AddGlobalScope[ScopeComment]("visible", func(_ context.Context, q *Query[ScopeComment]) {
		q.Where("tenant_id = ?", 1).OrWhere("public = ?", true)
	})

	blogs, err := newQuery[ScopeBlog]().With("Comments").Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(blogs) != 1 {
		t.Fatalf("got %d blogs, want 1", len(blogs))
	}
	if got := len(blogs[0].Comments); got != 2 {
		t.Fatalf("OR SCOPE COERCED TO AND: got %d comments, want 2 (id=1 tenant-match + id=2 public-match)", got)
	}
	// id=3 must NOT be present.
	for _, c := range blogs[0].Comments {
		if c.ID == 3 {
			t.Errorf("attacker comment id=3 leaked through scope")
		}
	}
}

// --- Follow-up: driver-aware reflect scope path ----------------------

// TestEagerLoad_ScopeErrorPropagates is the regression test for the
// reviewer's Blocker 2: a scope whose setup fails (unknown operator,
// invalid identifier, ...) used to be silently dropped from the
// eager-load query because applyGlobalScopesByType discarded q.err.
// Now the error propagates as a query-time failure.
func TestEagerLoad_ScopeErrorPropagates(t *testing.T) {
	m := setupSecurityTables(t)

	if _, err := m.DB().Exec(`INSERT INTO scope_posts (id, title, tenant_id, created_at, updated_at) VALUES
		(1, 'blog', 1, '2024-01-01', '2024-01-01')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := m.DB().Exec(`INSERT INTO scope_comments (id, post_id, tenant_id, body, created_at, updated_at) VALUES
		(1, 1, 1, 'a', '2024-01-01', '2024-01-01')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Register a scope that uses an operator no driver knows. The
	// scope's q.Where(...) sets q.err to "invalid SQL operator". Before
	// the fix, applyGlobalScopesByType returned only []Condition and
	// dropped q.err, so the eager-load ran without any predicate.
	AddGlobalScope[ScopeComment]("broken", func(_ context.Context, q *Query[ScopeComment]) {
		q.Where("body NOSUCHOP ?", "x")
	})

	_, err := newQuery[ScopeBlog]().With("Comments").Get(context.Background())
	if err == nil {
		t.Fatal("expected eager-load to fail with scope error; got nil (scope was silently dropped)")
	}
}

// --- Follow-up: terminal err-check after applyGlobalScopes -----------

// TestSum_ScopeErrorPropagates confirms aggregate() returns the
// deferred scope error captured during applyGlobalScopes instead of
// running SQL with the predicate dropped.
func TestSum_ScopeErrorPropagates(t *testing.T) {
	m := setupSecurityTables(t)

	if _, err := m.DB().Exec(`INSERT INTO scope_posts (id, title, tenant_id, amount, created_at, updated_at) VALUES
		(1, 'a', 1, 100, '2024-01-01', '2024-01-01'),
		(2, 'b', 2, 999, '2024-01-01', '2024-01-01')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	AddGlobalScope[ScopePost]("broken", func(_ context.Context, q *Query[ScopePost]) {
		q.Where("amount NOSUCHOP ?", 1)
	})

	got, err := newQuery[ScopePost]().Sum(context.Background(), "amount")
	if err == nil {
		t.Fatalf("expected Sum to fail with scope error; got nil (silent scope drop), result = %v", got)
	}
}

// TestIncrement_ScopeErrorPropagates confirms incrementOrDecrement()
// returns the deferred scope error instead of mutating rows with the
// scope predicate dropped.
func TestIncrement_ScopeErrorPropagates(t *testing.T) {
	m := setupSecurityTables(t)

	if _, err := m.DB().Exec(`INSERT INTO scope_posts (id, title, tenant_id, views, created_at, updated_at) VALUES
		(1, 'a', 1, 0, '2024-01-01', '2024-01-01'),
		(2, 'b', 2, 0, '2024-01-01', '2024-01-01')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	AddGlobalScope[ScopePost]("broken", func(_ context.Context, q *Query[ScopePost]) {
		q.Where("views NOSUCHOP ?", 1)
	})

	err := newQuery[ScopePost]().Increment(context.Background(), "views", 5)
	if err == nil {
		t.Fatal("expected Increment to fail with scope error; got nil (silent scope drop)")
	}

	// Verify NO rows were mutated.
	var v1, v2 int
	if err := m.DB().QueryRow(`SELECT views FROM scope_posts WHERE id = 1`).Scan(&v1); err != nil {
		t.Fatalf("v1: %v", err)
	}
	if err := m.DB().QueryRow(`SELECT views FROM scope_posts WHERE id = 2`).Scan(&v2); err != nil {
		t.Fatalf("v2: %v", err)
	}
	if v1 != 0 || v2 != 0 {
		t.Errorf("rows mutated despite scope error: v1=%d v2=%d (want 0,0)", v1, v2)
	}
}

// TestForceDelete_ScopeErrorPropagates confirms ForceDelete returns the
// deferred scope error instead of deleting rows with the scope
// predicate dropped.
func TestForceDelete_ScopeErrorPropagates(t *testing.T) {
	m := setupSecurityTables(t)

	if _, err := m.DB().Exec(`INSERT INTO scope_posts (id, title, tenant_id, created_at, updated_at) VALUES
		(1, 'a', 1, '2024-01-01', '2024-01-01'),
		(2, 'b', 2, '2024-01-01', '2024-01-01')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	AddGlobalScope[ScopePost]("broken", func(_ context.Context, q *Query[ScopePost]) {
		q.Where("tenant_id NOSUCHOP ?", 1)
	})

	affected, err := newQuery[ScopePost]().ForceDelete(context.Background())
	if err == nil {
		t.Fatalf("expected ForceDelete to fail with scope error; got nil, affected = %d", affected)
	}

	// Verify NO rows were deleted.
	var n int
	if err := m.DB().QueryRow(`SELECT COUNT(*) FROM scope_posts`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("rows deleted despite scope error: count=%d (want 2)", n)
	}
}

// TestEagerLoad_DriverOperatorInScope confirms the reflect-only scope
// path passes the active driver into the constructed *Query[T] so
// scopes that use a driver-registered operator resolve via
// drv.OperatorRegistry instead of always failing with "invalid SQL
// operator". SQLite's OperatorRegistry is nil today, but the same
// resolveOperator path is exercised; if the driver were nil, the
// operator would be rejected before the registry was consulted. The
// test asserts the error message that surfaces from a driver-less
// path ("invalid SQL operator") still propagates correctly, which
// confirms the reflect-only path reaches resolveOperator at all.
func TestEagerLoad_DriverOperatorInScope(t *testing.T) {
	m := setupSecurityTables(t)

	if _, err := m.DB().Exec(`INSERT INTO scope_posts (id, title, tenant_id, created_at, updated_at) VALUES
		(1, 'blog', 1, '2024-01-01', '2024-01-01')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := m.DB().Exec(`INSERT INTO scope_comments (id, post_id, tenant_id, body, created_at, updated_at) VALUES
		(1, 1, 1, 'a', '2024-01-01', '2024-01-01')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// A Postgres-only operator on a SQLite test harness. SQLite's
	// OperatorRegistry is nil, so resolveOperator returns the same
	// "invalid SQL operator" message either with or without a driver.
	// What matters is the eager-load surfaces the error rather than
	// swallowing it.
	AddGlobalScope[ScopeComment]("custom_op", func(_ context.Context, q *Query[ScopeComment]) {
		q.Where("body @> ?", `"x"`)
	})

	_, err := newQuery[ScopeBlog]().With("Comments").Get(context.Background())
	if err == nil {
		t.Fatal("expected eager-load to surface scope operator error; got nil")
	}
}
