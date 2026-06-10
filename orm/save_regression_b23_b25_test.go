package orm

import (
	"context"
	"testing"
)

// ---------------------------------------------------------------------------
// B25: CreateMany must write generated IDs/UUIDs/timestamps back to the
// caller's slice elements. Before the fix it saved a loop-variable copy,
// so records kept zero IDs and a later Save inserted a duplicate row
// (existence marking is pointer-keyed).
// ---------------------------------------------------------------------------

type b25User struct {
	Model[b25User]
	Name string `orm:"column:name"`
}

func (b25User) TableName() string { return "b25_users" }

type b25Doc struct {
	UUIDModel[b25Doc]
	Title string `orm:"column:title"`
}

func (b25Doc) TableName() string { return "b25_docs" }

type b25SoftUser struct {
	SoftDeleteModel[b25SoftUser]
	Name string `orm:"column:name"`
}

func (b25SoftUser) TableName() string { return "b25_soft_users" }

type b25SoftDoc struct {
	SoftDeleteUUIDModel[b25SoftDoc]
	Title string `orm:"column:title"`
}

func (b25SoftDoc) TableName() string { return "b25_soft_docs" }

func setupRegressionManager(t *testing.T) *Manager {
	t.Helper()
	manager := newTestManager(t)
	SetDefault(manager)
	t.Cleanup(func() {
		ResetDefault()
		manager.Shutdown(context.Background())
	})
	return manager
}

func mustExec(t *testing.T, m *Manager, query string) {
	t.Helper()
	if _, err := m.DB().Exec(query); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func countRows(t *testing.T, m *Manager, table string) int {
	t.Helper()
	var n int
	if err := m.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestCreateMany_WritesBackIDs_RegressionB25(t *testing.T) {
	manager := setupRegressionManager(t)
	ctx := context.Background()

	t.Run("Model", func(t *testing.T) {
		mustExec(t, manager, `CREATE TABLE b25_users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			created_at DATETIME,
			updated_at DATETIME
		)`)

		records := []b25User{{Name: "a"}, {Name: "b"}}
		if err := (Model[b25User]{}).CreateMany(ctx, records); err != nil {
			t.Fatalf("CreateMany: %v", err)
		}
		for i := range records {
			if records[i].ID == 0 {
				t.Fatalf("records[%d].ID = 0; generated ID not written back", i)
			}
			if records[i].CreatedAt.IsZero() {
				t.Errorf("records[%d].CreatedAt is zero; timestamp not written back", i)
			}
			if !IsExisting(&records[i]) {
				t.Errorf("records[%d] not marked existing", i)
			}
		}

		// A subsequent Save on the caller's element must UPDATE, not
		// INSERT a duplicate.
		records[0].Name = "a2"
		if err := Save(ctx, nil, &records[0]); err != nil {
			t.Fatalf("Save after CreateMany: %v", err)
		}
		if n := countRows(t, manager, "b25_users"); n != 2 {
			t.Fatalf("row count after re-Save = %d, want 2 (Save inserted a duplicate)", n)
		}
		var name string
		if err := manager.DB().QueryRow("SELECT name FROM b25_users WHERE id = ?", records[0].ID).Scan(&name); err != nil {
			t.Fatalf("reload: %v", err)
		}
		if name != "a2" {
			t.Errorf("name after re-Save = %q, want %q", name, "a2")
		}
	})

	t.Run("UUIDModel", func(t *testing.T) {
		mustExec(t, manager, `CREATE TABLE b25_docs (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			created_at DATETIME,
			updated_at DATETIME
		)`)

		records := []b25Doc{{Title: "a"}, {Title: "b"}}
		if err := (UUIDModel[b25Doc]{}).CreateMany(ctx, records); err != nil {
			t.Fatalf("CreateMany: %v", err)
		}
		for i := range records {
			if records[i].ID == "" {
				t.Fatalf("records[%d].ID empty; generated UUID not written back", i)
			}
			if !IsExisting(&records[i]) {
				t.Errorf("records[%d] not marked existing", i)
			}
		}

		records[0].Title = "a2"
		if err := Save(ctx, nil, &records[0]); err != nil {
			t.Fatalf("Save after CreateMany: %v", err)
		}
		if n := countRows(t, manager, "b25_docs"); n != 2 {
			t.Fatalf("row count after re-Save = %d, want 2 (Save inserted a duplicate)", n)
		}
	})

	t.Run("SoftDeleteModel", func(t *testing.T) {
		mustExec(t, manager, `CREATE TABLE b25_soft_users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME
		)`)

		records := []b25SoftUser{{Name: "a"}, {Name: "b"}}
		if err := (SoftDeleteModel[b25SoftUser]{}).CreateMany(ctx, records); err != nil {
			t.Fatalf("CreateMany: %v", err)
		}
		for i := range records {
			if records[i].ID == 0 {
				t.Fatalf("records[%d].ID = 0; generated ID not written back", i)
			}
		}

		records[0].Name = "a2"
		if err := Save(ctx, nil, &records[0]); err != nil {
			t.Fatalf("Save after CreateMany: %v", err)
		}
		if n := countRows(t, manager, "b25_soft_users"); n != 2 {
			t.Fatalf("row count after re-Save = %d, want 2 (Save inserted a duplicate)", n)
		}
	})

	t.Run("SoftDeleteUUIDModel", func(t *testing.T) {
		mustExec(t, manager, `CREATE TABLE b25_soft_docs (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME
		)`)

		records := []b25SoftDoc{{Title: "a"}, {Title: "b"}}
		if err := (SoftDeleteUUIDModel[b25SoftDoc]{}).CreateMany(ctx, records); err != nil {
			t.Fatalf("CreateMany: %v", err)
		}
		for i := range records {
			if records[i].ID == "" {
				t.Fatalf("records[%d].ID empty; generated UUID not written back", i)
			}
		}

		records[0].Title = "a2"
		if err := Save(ctx, nil, &records[0]); err != nil {
			t.Fatalf("Save after CreateMany: %v", err)
		}
		if n := countRows(t, manager, "b25_soft_docs"); n != 2 {
			t.Fatalf("row count after re-Save = %d, want 2 (Save inserted a duplicate)", n)
		}
	})
}

// ---------------------------------------------------------------------------
// B23: Save's UPDATE is a by-primary-key instance write and must bypass
// global scopes deterministically. Before the fix, the soft-delete scope
// was registered lazily by newQuery[T], so saving a trashed row succeeded
// when no newQuery[T] had run in the process but silently 0-row-updated
// after one had. Both orders must now behave identically: the UPDATE
// takes effect.
// ---------------------------------------------------------------------------

// b23ScopedUser exercises the order where the soft-delete scope IS
// registered before Save (a read forces registration first).
type b23ScopedUser struct {
	SoftDeleteModel[b23ScopedUser]
	Name string `orm:"column:name"`
}

func (b23ScopedUser) TableName() string { return "b23_scoped_users" }

// b23FreshUser exercises the order where NO newQuery-backed read ever
// ran for the type before Save, so the scope is unregistered.
type b23FreshUser struct {
	SoftDeleteModel[b23FreshUser]
	Name string `orm:"column:name"`
}

func (b23FreshUser) TableName() string { return "b23_fresh_users" }

// b23ScopedDoc covers the saveUUIDModel update branch with the scope
// registered.
type b23ScopedDoc struct {
	SoftDeleteUUIDModel[b23ScopedDoc]
	Title string `orm:"column:title"`
}

func (b23ScopedDoc) TableName() string { return "b23_scoped_docs" }

func TestSave_TrashedRow_Deterministic_RegressionB23(t *testing.T) {
	manager := setupRegressionManager(t)
	ctx := context.Background()

	t.Run("ScopeRegisteredFirst", func(t *testing.T) {
		mustExec(t, manager, `CREATE TABLE b23_scoped_users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME
		)`)

		// Force soft-delete scope registration via a throwaway
		// newQuery-backed read BEFORE the save under test.
		if _, err := (SoftDeleteModel[b23ScopedUser]{}).Count(ctx); err != nil {
			t.Fatalf("Count (scope registration): %v", err)
		}

		u := &b23ScopedUser{Name: "before"}
		if err := Save(ctx, nil, u); err != nil {
			t.Fatalf("insert: %v", err)
		}
		mustExec(t, manager, "UPDATE b23_scoped_users SET deleted_at = datetime('now')")

		u.Name = "after"
		if err := Save(ctx, nil, u); err != nil {
			t.Fatalf("Save on trashed row: %v", err)
		}

		var got b23ScopedUser
		if err := (SoftDeleteModel[b23ScopedUser]{}).Where("id = ?", u.ID).WithTrashed().First(ctx, &got); err != nil {
			t.Fatalf("reload WithTrashed: %v", err)
		}
		if got.Name != "after" {
			t.Errorf("name after Save on trashed row = %q, want %q (UPDATE silently filtered by soft-delete scope)", got.Name, "after")
		}
	})

	t.Run("NoPriorReads", func(t *testing.T) {
		mustExec(t, manager, `CREATE TABLE b23_fresh_users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME
		)`)

		// No newQuery-backed read for b23FreshUser before this Save:
		// the soft-delete scope is unregistered for the type.
		u := &b23FreshUser{Name: "before"}
		if err := Save(ctx, nil, u); err != nil {
			t.Fatalf("insert: %v", err)
		}
		mustExec(t, manager, "UPDATE b23_fresh_users SET deleted_at = datetime('now')")

		u.Name = "after"
		if err := Save(ctx, nil, u); err != nil {
			t.Fatalf("Save on trashed row: %v", err)
		}

		var got b23FreshUser
		if err := (SoftDeleteModel[b23FreshUser]{}).Where("id = ?", u.ID).WithTrashed().First(ctx, &got); err != nil {
			t.Fatalf("reload WithTrashed: %v", err)
		}
		if got.Name != "after" {
			t.Errorf("name after Save on trashed row = %q, want %q", got.Name, "after")
		}
	})

	t.Run("UUIDScopeRegisteredFirst", func(t *testing.T) {
		mustExec(t, manager, `CREATE TABLE b23_scoped_docs (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME
		)`)

		if _, err := (SoftDeleteUUIDModel[b23ScopedDoc]{}).Count(ctx); err != nil {
			t.Fatalf("Count (scope registration): %v", err)
		}

		d := &b23ScopedDoc{Title: "before"}
		if err := Save(ctx, nil, d); err != nil {
			t.Fatalf("insert: %v", err)
		}
		mustExec(t, manager, "UPDATE b23_scoped_docs SET deleted_at = datetime('now')")

		d.Title = "after"
		if err := Save(ctx, nil, d); err != nil {
			t.Fatalf("Save on trashed row: %v", err)
		}

		var got b23ScopedDoc
		if err := (SoftDeleteUUIDModel[b23ScopedDoc]{}).Where("id = ?", d.ID).WithTrashed().First(ctx, &got); err != nil {
			t.Fatalf("reload WithTrashed: %v", err)
		}
		if got.Title != "after" {
			t.Errorf("title after Save on trashed row = %q, want %q (UPDATE silently filtered by soft-delete scope)", got.Title, "after")
		}
	})
}
