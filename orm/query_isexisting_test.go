package orm

import (
	"context"
	"errors"
	"testing"
)

// updateHookUser exercises the BeforeUpdate/AfterUpdate path through
// the vanilla read-mutate-Save round trip. The hooks are skipped when
// IsExisting is false (Save takes the INSERT branch) so they double
// as a witness for the IsExisting fix.
type updateHookUser struct {
	Model[updateHookUser]
	Name string `orm:"column:name"`
}

func (updateHookUser) TableName() string { return "update_hook_users" }

var vanillaUpdateHooks struct {
	beforeUpdate bool
	afterUpdate  bool
}

func (m *updateHookUser) BeforeUpdate() error {
	vanillaUpdateHooks.beforeUpdate = true
	return nil
}

func (m *updateHookUser) AfterUpdate() error {
	vanillaUpdateHooks.afterUpdate = true
	return nil
}

// roundTripSoftUser is a SoftDeleteModel-backed sample for the
// soft-delete round-trip regression. SoftDelete shares the IsExisting
// path with Model so this is more about confirming the marker fires
// across all 4 mutable base types via method promotion.
type roundTripSoftUser struct {
	SoftDeleteModel[roundTripSoftUser]
	Name string `orm:"column:name"`
}

func (roundTripSoftUser) TableName() string { return "rt_soft_users" }

// roundTripUUIDUser exercises markExisting via UUIDModel.setExisting.
type roundTripUUIDUser struct {
	UUIDModel[roundTripUUIDUser]
	Name string `orm:"column:name"`
}

func (roundTripUUIDUser) TableName() string { return "rt_uuid_users" }

// roundTripSoftUUIDUser exercises markExisting via
// SoftDeleteUUIDModel.setExisting, the last of the four mutable base
// types.
type roundTripSoftUUIDUser struct {
	SoftDeleteUUIDModel[roundTripSoftUUIDUser]
	Name string `orm:"column:name"`
}

func (roundTripSoftUUIDUser) TableName() string { return "rt_soft_uuid_users" }

// nestedBase wraps Model[T] with extra columns common across an app's
// models. The IsExisting fix must still resolve through one extra
// level of embedding so app-level base types behave the same as the
// raw Model[T] case.
type nestedBase[T any] struct {
	Model[T]
	Tenant string `orm:"column:tenant"`
}

type nestedUser struct {
	nestedBase[nestedUser]
	Name string `orm:"column:name"`
}

func (nestedUser) TableName() string { return "nested_users" }

// setupIsExistingTest provisions an in-memory sqlite manager with the
// users table that the package-level User model in orm_test.go targets.
// It installs the manager as the default so static-style helpers
// (User{}.Where(...).First(...)) resolve, and restores the prior
// default in cleanup so it does not bleed into sibling tests.
func setupIsExistingTest(t *testing.T) *Manager {
	t.Helper()
	m := newTestManager(t)

	if _, err := m.DB().Exec(`CREATE TABLE users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		email TEXT UNIQUE NOT NULL,
		age INTEGER,
		created_at DATETIME,
		updated_at DATETIME,
		deleted_at DATETIME
	)`); err != nil {
		t.Fatalf("create users table: %v", err)
	}

	prev := Default()
	SetDefault(m)
	t.Cleanup(func() {
		if prev != nil {
			SetDefault(prev)
		} else {
			ResetDefault()
		}
		m.Shutdown(context.Background())
	})
	return m
}

// TestQuery_FirstMarksIsExisting is the regression for a bug where
// Query.Get's "mark as existing" branch type-asserted &model to
// *Model[T]. T is the embedding struct, so the assertion always
// failed and IsExisting stayed false. A subsequent Save then took
// the INSERT path and duplicated the row instead of updating it.
func TestQuery_FirstMarksIsExisting(t *testing.T) {
	m := setupIsExistingTest(t)

	original := &User{Name: "Alice", Email: "alice@example.com", Age: 30}
	if err := Save(context.Background(), m, original); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	if original.Model.ID == 0 {
		t.Fatalf("expected seeded user to have an ID, got 0")
	}

	var u User
	if err := (User{}).Where("id = ?", original.Model.ID).First(context.Background(), &u); err != nil {
		t.Fatalf("First returned error: %v", err)
	}

	if !u.Model.IsExisting {
		t.Fatalf("First did not mark IsExisting=true; subsequent Save would re-insert")
	}

	// Mutating and saving must UPDATE the existing row, not insert a
	// duplicate. Pre-fix this would yield COUNT(*) = 2.
	u.Name = "Alice Renamed"
	if err := Save(context.Background(), m, &u); err != nil {
		t.Fatalf("Save after First: %v", err)
	}

	var count int
	if err := m.DB().QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 row after update, got %d (Save inserted instead of updating)", count)
	}

	var name string
	if err := m.DB().QueryRow(`SELECT name FROM users WHERE id = ?`, original.Model.ID).Scan(&name); err != nil {
		t.Fatalf("read back name: %v", err)
	}
	if name != "Alice Renamed" {
		t.Fatalf("name = %q, want %q", name, "Alice Renamed")
	}
}

// TestImmutableModel_FindThenSaveReturnsImmutableErr is the auto-inc
// counterpart of the existing UUID test. Without setExisting on
// *ImmutableModel[T], a Find->Save round trip would silently
// re-INSERT (auto-inc generates a new ID), corrupting append-only
// chains. The fix gives the ErrImmutableModelUpdate guard a chance
// to fire on the read result.
func TestImmutableModel_FindThenSaveReturnsImmutableErr(t *testing.T) {
	m := setupImmutableTests(t)

	rec := &AuditLog{Action: "seed"}
	if err := Save(context.Background(), m, rec); err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	got, err := ImmutableModel[AuditLog]{}.Find(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if !got.IsExisting {
		t.Fatal("Find did not mark IsExisting on ImmutableModel; re-Save would silently duplicate")
	}
	if err := Save(context.Background(), m, got); !errors.Is(err, ErrImmutableModelUpdate) {
		t.Errorf("re-Save via Find result error = %v, want ErrImmutableModelUpdate", err)
	}

	var count int
	if err := m.DB().QueryRow(`SELECT COUNT(*) FROM audit_logs`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 row after blocked update, got %d (silent duplicate)", count)
	}
}

// TestSoftDeleteModel_FirstMarksIsExisting confirms the marker fires
// across the four mutable base types via method promotion. SoftDelete
// shares the persistence path with Model[T] so this test is a witness
// for the embedding-correct interface dispatch, not a separate code
// path on the write side.
func TestSoftDeleteModel_FirstMarksIsExisting(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.DB().Exec(`CREATE TABLE rt_soft_users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		created_at DATETIME,
		updated_at DATETIME,
		deleted_at DATETIME
	)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	prev := Default()
	SetDefault(m)
	t.Cleanup(func() {
		if prev != nil {
			SetDefault(prev)
		} else {
			ResetDefault()
		}
		m.Shutdown(context.Background())
	})

	rec := &roundTripSoftUser{Name: "seed"}
	if err := Save(context.Background(), m, rec); err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	var got roundTripSoftUser
	if err := (roundTripSoftUser{}).Where("id = ?", rec.ID).First(context.Background(), &got); err != nil {
		t.Fatalf("First: %v", err)
	}
	if !got.IsExisting {
		t.Fatal("First did not mark IsExisting on SoftDeleteModel")
	}
	got.Name = "renamed"
	if err := Save(context.Background(), m, &got); err != nil {
		t.Fatalf("Save after First: %v", err)
	}

	var count int
	if err := m.DB().QueryRow(`SELECT COUNT(*) FROM rt_soft_users`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 row after update, got %d", count)
	}
}

// TestModel_FirstThenSaveFiresUpdateHooks closes the hook-coverage
// gap: BeforeUpdate / AfterUpdate fire on the vanilla read-mutate-Save
// round trip only when First marks IsExisting=true. Pre-fix, Save
// took the INSERT branch and skipped these hooks entirely.
func TestModel_FirstThenSaveFiresUpdateHooks(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.DB().Exec(`CREATE TABLE update_hook_users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		created_at DATETIME,
		updated_at DATETIME
	)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	prev := Default()
	SetDefault(m)
	t.Cleanup(func() {
		if prev != nil {
			SetDefault(prev)
		} else {
			ResetDefault()
		}
		m.Shutdown(context.Background())
	})

	rec := &updateHookUser{Name: "v1"}
	if err := Save(context.Background(), m, rec); err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	vanillaUpdateHooks.beforeUpdate = false
	vanillaUpdateHooks.afterUpdate = false

	var got updateHookUser
	if err := (updateHookUser{}).Where("id = ?", rec.ID).First(context.Background(), &got); err != nil {
		t.Fatalf("First: %v", err)
	}
	got.Name = "v2"
	if err := Save(context.Background(), m, &got); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if !vanillaUpdateHooks.beforeUpdate {
		t.Error("BeforeUpdate did not fire on read-mutate-Save round trip")
	}
	if !vanillaUpdateHooks.afterUpdate {
		t.Error("AfterUpdate did not fire on read-mutate-Save round trip")
	}
}

// TestUUIDModel_FirstMarksIsExisting verifies marker fires through
// the UUID base type. Save uses RETURNING-id semantics on Postgres
// and string PK on sqlite; either way IsExisting on read is the
// gating condition for re-Save -> UPDATE.
func TestUUIDModel_FirstMarksIsExisting(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.DB().Exec(`CREATE TABLE rt_uuid_users (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		created_at DATETIME,
		updated_at DATETIME
	)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	prev := Default()
	SetDefault(m)
	t.Cleanup(func() {
		if prev != nil {
			SetDefault(prev)
		} else {
			ResetDefault()
		}
		m.Shutdown(context.Background())
	})

	rec := &roundTripUUIDUser{Name: "seed"}
	if err := Save(context.Background(), m, rec); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	if rec.ID == "" {
		t.Fatal("UUID was not assigned on insert")
	}

	var got roundTripUUIDUser
	if err := (roundTripUUIDUser{}).Where("id = ?", rec.ID).First(context.Background(), &got); err != nil {
		t.Fatalf("First: %v", err)
	}
	if !got.IsExisting {
		t.Fatal("First did not mark IsExisting on UUIDModel")
	}
	got.Name = "renamed"
	if err := Save(context.Background(), m, &got); err != nil {
		t.Fatalf("Save after First: %v", err)
	}
	var count int
	if err := m.DB().QueryRow(`SELECT COUNT(*) FROM rt_uuid_users`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 row after update, got %d", count)
	}
}

// TestSoftDeleteUUIDModel_FirstMarksIsExisting closes the last of the
// four mutable base types.
func TestSoftDeleteUUIDModel_FirstMarksIsExisting(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.DB().Exec(`CREATE TABLE rt_soft_uuid_users (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		created_at DATETIME,
		updated_at DATETIME,
		deleted_at DATETIME
	)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	prev := Default()
	SetDefault(m)
	t.Cleanup(func() {
		if prev != nil {
			SetDefault(prev)
		} else {
			ResetDefault()
		}
		m.Shutdown(context.Background())
	})

	rec := &roundTripSoftUUIDUser{Name: "seed"}
	if err := Save(context.Background(), m, rec); err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	var got roundTripSoftUUIDUser
	if err := (roundTripSoftUUIDUser{}).Where("id = ?", rec.ID).First(context.Background(), &got); err != nil {
		t.Fatalf("First: %v", err)
	}
	if !got.IsExisting {
		t.Fatal("First did not mark IsExisting on SoftDeleteUUIDModel")
	}
}

// TestNestedEmbedding_MarkExistingPromotes documents that markExisting
// resolves through two-level embedding via Go method promotion.
// Save itself does NOT support nested embedding (Save's reflection
// walk only inspects immediate fields), so this test asserts only the
// interface dispatch piece, not a full round trip. If/when Save is
// extended to walk nested embeds, this test continues to hold.
func TestNestedEmbedding_MarkExistingPromotes(t *testing.T) {
	rec := &nestedUser{Name: "x"}
	if rec.IsExisting {
		t.Fatal("zero value should have IsExisting=false")
	}
	markExisting(rec)
	if !rec.IsExisting {
		t.Fatal("markExisting did not promote setExisting through nested embedding")
	}
}

// TestFirstOrCreate_HitBranchUpdatesNotDuplicates closes the
// firstOrCreate hit-branch surface: when the lookup hits, the
// returned *T must round-trip IsExisting so a downstream Save
// updates rather than re-inserting. Pre-fix the bug surfaced here as
// silent duplicate INSERTs.
func TestFirstOrCreate_HitBranchUpdatesNotDuplicates(t *testing.T) {
	m := setupIsExistingTest(t)

	if _, err := (User{}).FirstOrCreate(context.Background(),
		map[string]any{"email": "alice@example.com"},
		map[string]any{"name": "Alice", "age": 30},
	); err != nil {
		t.Fatalf("seed FirstOrCreate: %v", err)
	}

	got, err := (User{}).FirstOrCreate(context.Background(),
		map[string]any{"email": "alice@example.com"},
		map[string]any{"name": "ignored", "age": 99},
	)
	if err != nil {
		t.Fatalf("hit FirstOrCreate: %v", err)
	}
	if !got.Model.IsExisting {
		t.Fatal("FirstOrCreate hit branch did not return IsExisting=true")
	}

	got.Name = "Renamed"
	if err := Save(context.Background(), m, got); err != nil {
		t.Fatalf("Save after hit: %v", err)
	}

	var count int
	if err := m.DB().QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 row after update, got %d (FirstOrCreate hit + Save duplicated)", count)
	}
}

// TestRawQuery_FirstMarksIsExisting covers the same bug shape on the
// raw-SQL escape hatch. Apps reaching for NewRawQuery[User](sql) for
// hand-written joins still expect a downstream Save to UPDATE; without
// markExisting on the scanned destination the row would be duplicated.
func TestRawQuery_FirstMarksIsExisting(t *testing.T) {
	m := setupIsExistingTest(t)

	original := &User{Name: "Alice", Email: "alice@example.com", Age: 30}
	if err := Save(context.Background(), m, original); err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	rq := NewRawQuery[User](`SELECT * FROM users WHERE id = ?`, original.Model.ID)
	rq.driver = m.DefaultDriver()

	var u User
	if err := rq.First(context.Background(), &u); err != nil {
		t.Fatalf("RawQuery.First: %v", err)
	}
	if !u.Model.IsExisting {
		t.Fatalf("RawQuery.First did not mark IsExisting=true; subsequent Save would re-insert")
	}

	u.Name = "Alice Renamed"
	if err := Save(context.Background(), m, &u); err != nil {
		t.Fatalf("Save after RawQuery.First: %v", err)
	}

	var count int
	if err := m.DB().QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 row after update, got %d (RawQuery + Save inserted instead of updating)", count)
	}
}

// TestRawQuery_GetMarksIsExisting covers the slice variant.
func TestRawQuery_GetMarksIsExisting(t *testing.T) {
	m := setupIsExistingTest(t)

	seed := []User{
		{Name: "Alice", Email: "alice@example.com", Age: 30},
		{Name: "Bob", Email: "bob@example.com", Age: 25},
	}
	for i := range seed {
		if err := Save(context.Background(), m, &seed[i]); err != nil {
			t.Fatalf("seed Save[%d]: %v", i, err)
		}
	}

	rq := NewRawQuery[User](`SELECT * FROM users ORDER BY id`)
	rq.driver = m.DefaultDriver()

	results, err := rq.Get(context.Background())
	if err != nil {
		t.Fatalf("RawQuery.Get: %v", err)
	}
	if len(results) != len(seed) {
		t.Fatalf("expected %d results, got %d", len(seed), len(results))
	}
	for i, r := range results {
		if !r.Model.IsExisting {
			t.Errorf("result[%d] (%s): IsExisting should be true", i, r.Email)
		}
	}
}

// TestQuery_GetMarksIsExisting covers the Get terminal directly: every
// element of the returned slice must have IsExisting set so callers
// iterating results can Save each one without forcing a duplicate
// insert.
func TestQuery_GetMarksIsExisting(t *testing.T) {
	m := setupIsExistingTest(t)

	seed := []User{
		{Name: "Alice", Email: "alice@example.com", Age: 30},
		{Name: "Bob", Email: "bob@example.com", Age: 25},
		{Name: "Carol", Email: "carol@example.com", Age: 40},
	}
	for i := range seed {
		if err := Save(context.Background(), m, &seed[i]); err != nil {
			t.Fatalf("seed Save[%d]: %v", i, err)
		}
	}

	q := newQuery[User]().Where("age > ?", 20)
	results, err := q.Get(context.Background())
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if len(results) != len(seed) {
		t.Fatalf("expected %d results, got %d", len(seed), len(results))
	}
	for i, r := range results {
		if !r.Model.IsExisting {
			t.Errorf("result[%d] (%s): IsExisting should be true", i, r.Email)
		}
	}
}
