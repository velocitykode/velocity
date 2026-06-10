package orm

import (
	"context"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Review finding: Save's UPDATE path set globalScopesApplied=true, which
// bypassed EVERY global scope, not just soft-delete. A by-PK Save on a
// model with a tenant scope could mutate a row belonging to another
// tenant. The fix mirrors ForceDelete: skip ONLY the soft-delete scope by
// name; all other registered scopes still constrain the UPDATE.
// ---------------------------------------------------------------------------

type tenantSavePost struct {
	SoftDeleteModel[tenantSavePost]
	Title    string `orm:"column:title"`
	TenantID int    `orm:"column:tenant_id"`
}

func (tenantSavePost) TableName() string { return "tenant_save_posts" }

func TestSave_HonoursNonSoftDeleteGlobalScopes(t *testing.T) {
	manager := setupRegressionManager(t)
	ctx := context.Background()
	t.Cleanup(func() { clearGlobalScopes[tenantSavePost]() })

	mustExec(t, manager, `CREATE TABLE tenant_save_posts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		title TEXT NOT NULL,
		tenant_id INTEGER NOT NULL,
		created_at DATETIME,
		updated_at DATETIME,
		deleted_at DATETIME
	)`)

	AddGlobalScope[tenantSavePost]("tenant", func(_ context.Context, q *Query[tenantSavePost]) {
		q.Where("tenant_id = ?", 1)
	})

	// Seed one row per tenant directly so the scope cannot interfere
	// with the fixture itself.
	mustExec(t, manager, `INSERT INTO tenant_save_posts (id, title, tenant_id)
		VALUES (1, 'mine', 1), (2, 'theirs', 2)`)

	titleOf := func(id int) string {
		t.Helper()
		var title string
		if err := manager.DB().QueryRow(
			"SELECT title FROM tenant_save_posts WHERE id = ?", id).Scan(&title); err != nil {
			t.Fatalf("reload id=%d: %v", id, err)
		}
		return title
	}

	t.Run("in-scope row updates", func(t *testing.T) {
		mine := &tenantSavePost{Title: "mine-updated", TenantID: 1}
		mine.ID = 1
		markModelExisting(mine)
		if err := Save(ctx, nil, mine); err != nil {
			t.Fatalf("Save in-scope: %v", err)
		}
		if got := titleOf(1); got != "mine-updated" {
			t.Errorf("in-scope title = %q, want %q", got, "mine-updated")
		}
	})

	t.Run("out-of-scope row is not touched", func(t *testing.T) {
		theirs := &tenantSavePost{Title: "hijacked", TenantID: 2}
		theirs.ID = 2
		markModelExisting(theirs)
		if err := Save(ctx, nil, theirs); err != nil {
			t.Fatalf("Save out-of-scope: %v", err)
		}
		if got := titleOf(2); got != "theirs" {
			t.Errorf("out-of-scope title = %q, want %q (tenant scope bypassed by Save)", got, "theirs")
		}
	})

	t.Run("trashed in-scope row still updates", func(t *testing.T) {
		// The soft-delete scope must remain skipped (B23 semantics):
		// only the OTHER scopes constrain the by-PK write.
		mustExec(t, manager, "UPDATE tenant_save_posts SET deleted_at = datetime('now') WHERE id = 1")
		mine := &tenantSavePost{Title: "mine-trashed-updated", TenantID: 1}
		mine.ID = 1
		markModelExisting(mine)
		if err := Save(ctx, nil, mine); err != nil {
			t.Fatalf("Save trashed in-scope: %v", err)
		}
		if got := titleOf(1); got != "mine-trashed-updated" {
			t.Errorf("trashed in-scope title = %q, want %q (soft-delete scope filtered a by-PK Save)", got, "mine-trashed-updated")
		}
	})
}

// ---------------------------------------------------------------------------
// Review finding: the grammars type-assert cond.Value.([]any) when
// expanding IN/NOT IN placeholders, so a typed slice ([]int, []string)
// fell through to the empty-list constant rewrite:
// Where("id NOT IN ?", []int{1}) compiled to the always-true 1=1 and the
// IN form to the never-true 1=0. normalizeMultiValue now flattens any
// slice/array kind to []any at build time and hard-errors on non-slice
// values and wrong-arity BETWEEN bounds.
// ---------------------------------------------------------------------------

type mvUser struct {
	Model[mvUser]
	Name string `orm:"column:name"`
}

func (mvUser) TableName() string { return "mv_users" }

func TestWhere_TypedSliceMultiValue_EndToEnd(t *testing.T) {
	manager := setupRegressionManager(t)
	_ = manager
	ctx := context.Background()

	mustExec(t, manager, `CREATE TABLE mv_users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		created_at DATETIME,
		updated_at DATETIME
	)`)
	mustExec(t, manager, `INSERT INTO mv_users (id, name, created_at, updated_at) VALUES
		(1, 'a', datetime('now'), datetime('now')),
		(2, 'b', datetime('now'), datetime('now')),
		(3, 'c', datetime('now'), datetime('now'))`)

	t.Run("NOT IN with typed slice excludes listed rows", func(t *testing.T) {
		got, err := (Model[mvUser]{}).Where("id NOT IN ?", []int{1}).Get(ctx)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("NOT IN []int{1} returned %d rows, want 2 (typed slice mistaken for empty list compiles to 1=1)", len(got))
		}
		for _, u := range got {
			if u.ID == 1 {
				t.Errorf("row id=1 returned despite NOT IN [1]")
			}
		}
	})

	t.Run("IN with typed slice matches listed rows", func(t *testing.T) {
		got, err := (Model[mvUser]{}).Where("id IN ?", []int64{2, 3}).Get(ctx)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("IN []int64{2,3} returned %d rows, want 2 (typed slice mistaken for empty list compiles to 1=0)", len(got))
		}
	})

	t.Run("empty typed slice keeps empty-list semantics", func(t *testing.T) {
		got, err := (Model[mvUser]{}).Where("id IN ?", []int{}).Get(ctx)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("IN empty typed slice returned %d rows, want 0", len(got))
		}
	})
}

func TestWhere_MultiValueOperatorBuildErrors(t *testing.T) {
	wantErrContaining := func(t *testing.T, q *Query[mvUser], substr string) {
		t.Helper()
		if q.Err() == nil {
			t.Fatalf("expected build error containing %q, got nil", substr)
		}
		if !strings.Contains(q.Err().Error(), substr) {
			t.Errorf("error = %q, want it to contain %q", q.Err().Error(), substr)
		}
	}

	t.Run("scalar value for IN is rejected", func(t *testing.T) {
		q := (&Query[mvUser]{table: "mv_users"}).Where("id IN ?", 5)
		wantErrContaining(t, q, "requires a slice")
	})

	t.Run("byte slice for IN is rejected as scalar blob", func(t *testing.T) {
		q := (&Query[mvUser]{table: "mv_users"}).Where("id IN ?", []byte("ab"))
		wantErrContaining(t, q, "[]byte")
	})

	t.Run("BETWEEN with wrong arity is rejected", func(t *testing.T) {
		q := (&Query[mvUser]{table: "mv_users"}).Where("id BETWEEN ?", []any{1})
		wantErrContaining(t, q, "exactly two values")
	})

	t.Run("BETWEEN with typed array normalizes", func(t *testing.T) {
		q := (&Query[mvUser]{table: "mv_users"}).Where("id BETWEEN ?", [2]int{1, 9})
		if q.Err() != nil {
			t.Fatalf("unexpected error: %v", q.Err())
		}
		vs, ok := q.conditions[0].Value.([]any)
		if !ok || len(vs) != 2 {
			t.Fatalf("Value = %#v, want []any of len 2", q.conditions[0].Value)
		}
	})

	t.Run("OrWhere typed slice normalizes", func(t *testing.T) {
		q := (&Query[mvUser]{table: "mv_users"}).Where("name = ?", "a").OrWhere("id IN ?", []int{1, 2})
		if q.Err() != nil {
			t.Fatalf("unexpected error: %v", q.Err())
		}
		vs, ok := q.conditions[1].Value.([]any)
		if !ok || len(vs) != 2 {
			t.Fatalf("Value = %#v, want []any of len 2", q.conditions[1].Value)
		}
	})

	t.Run("Having typed slice normalizes", func(t *testing.T) {
		q := (&Query[mvUser]{table: "mv_users"}).Having("id IN ?", []int{1, 2})
		if q.Err() != nil {
			t.Fatalf("unexpected error: %v", q.Err())
		}
		vs, ok := q.having[0].Value.([]any)
		if !ok || len(vs) != 2 {
			t.Fatalf("Value = %#v, want []any of len 2", q.having[0].Value)
		}
	})
}
