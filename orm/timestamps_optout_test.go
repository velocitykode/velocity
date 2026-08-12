package orm_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/velocitykode/velocity/orm"
	ormtesting "github.com/velocitykode/velocity/orm/testing"
)

// catalogRelease embeds the standard Model[T] (integer PK + Timestamps) but
// opts OUT of automatic timestamp management via UsesTimestamps()==false. Its
// backing table has no created_at/updated_at columns - only an id and a plain
// column - so any write or read path that referenced a timestamp column would
// fail against SQLite with "no such column", making a passing test proof that
// no such reference is emitted.
type catalogRelease struct {
	orm.Model[catalogRelease]
	Version string `orm:"column:version"`
}

func (catalogRelease) TableName() string { return "catalog_releases" }

func (catalogRelease) AssignableFields() []string { return []string{"version"} }

// UsesTimestamps opts the model out of created_at/updated_at management.
func (catalogRelease) UsesTimestamps() bool { return false }

// managedWidget is the counter-model: a normal Model[T] with timestamps
// managed, used to prove the opt-out does not regress default behavior.
type managedWidget struct {
	orm.Model[managedWidget]
	Name string `orm:"column:name"`
}

func (managedWidget) TableName() string { return "managed_widgets" }

// catalogReleaseUUID is a UUID-keyed model that opts out of timestamps. UUID
// Last() must not order by created_at for it (the column does not exist).
type catalogReleaseUUID struct {
	orm.UUIDModel[catalogReleaseUUID]
	Version string `orm:"column:version"`
}

func (catalogReleaseUUID) TableName() string    { return "catalog_release_uuids" }
func (catalogReleaseUUID) UsesTimestamps() bool { return false }

// catalogReleaseSoftUUID is a soft-deletable UUID model that opts out of
// timestamps. deleted_at survives the opt-out; created_at does not.
type catalogReleaseSoftUUID struct {
	orm.SoftDeleteUUIDModel[catalogReleaseSoftUUID]
	Version string `orm:"column:version"`
}

func (catalogReleaseSoftUUID) TableName() string    { return "catalog_release_soft_uuids" }
func (catalogReleaseSoftUUID) UsesTimestamps() bool { return false }

// managedReleaseUUID is the counter-model: a normal UUID model whose Last()
// must still order by created_at.
type managedReleaseUUID struct {
	orm.UUIDModel[managedReleaseUUID]
	Version string `orm:"column:version"`
}

func (managedReleaseUUID) TableName() string { return "managed_release_uuids" }

// setupOptOutTest builds an in-memory SQLite manager with the two fixture
// tables and installs it as the package default so the static-like model
// helpers resolve. The catalog_releases table deliberately has no
// created_at/updated_at columns.
func setupOptOutTest(t *testing.T) *orm.Manager {
	t.Helper()
	manager, err := orm.NewManager(orm.ManagerConfig{Driver: "sqlite", Database: ":memory:"})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	db := manager.DB()
	if _, err := db.Exec(`CREATE TABLE catalog_releases (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		version TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create catalog_releases: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE managed_widgets (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		created_at DATETIME,
		updated_at DATETIME
	)`); err != nil {
		t.Fatalf("create managed_widgets: %v", err)
	}
	// UUID-keyed fixtures for Last() coverage. The opt-out tables have no
	// created_at column; the managed one does.
	if _, err := db.Exec(`CREATE TABLE catalog_release_uuids (
		id TEXT PRIMARY KEY,
		version TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create catalog_release_uuids: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE catalog_release_soft_uuids (
		id TEXT PRIMARY KEY,
		version TEXT NOT NULL,
		deleted_at DATETIME
	)`); err != nil {
		t.Fatalf("create catalog_release_soft_uuids: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE managed_release_uuids (
		id TEXT PRIMARY KEY,
		version TEXT NOT NULL,
		created_at DATETIME,
		updated_at DATETIME
	)`); err != nil {
		t.Fatalf("create managed_release_uuids: %v", err)
	}
	orm.SetDefault(manager)
	t.Cleanup(func() {
		orm.ResetDefault()
		manager.Shutdown(context.Background())
	})
	return manager
}

// TestTimestampsOptOut_Meta proves the created_at/updated_at columns are
// dropped from the opted-out model's column set, while the plain column and id
// survive.
func TestTimestampsOptOut_Meta(t *testing.T) {
	meta := orm.MetaFor(reflect.TypeOf(catalogRelease{}))
	if meta == nil {
		t.Fatal("nil meta for catalogRelease")
	}
	if got := meta.ColumnFor("CreatedAt"); got != "" {
		t.Errorf("CreatedAt should be dropped, got column %q", got)
	}
	if got := meta.ColumnFor("UpdatedAt"); got != "" {
		t.Errorf("UpdatedAt should be dropped, got column %q", got)
	}
	if got := meta.FieldFor("created_at"); got != "" {
		t.Errorf("created_at column should not map to a field, got %q", got)
	}
	if got := meta.FieldFor("updated_at"); got != "" {
		t.Errorf("updated_at column should not map to a field, got %q", got)
	}
	if got := meta.ColumnFor("Version"); got != "version" {
		t.Errorf("Version column = %q, want \"version\"", got)
	}
	if got := meta.ColumnFor("ID"); got != "id" {
		t.Errorf("ID column = %q, want \"id\"", got)
	}

	// The managed counter-model still carries both timestamp columns.
	wmeta := orm.MetaFor(reflect.TypeOf(managedWidget{}))
	if wmeta.ColumnFor("CreatedAt") != "created_at" {
		t.Errorf("managedWidget CreatedAt column = %q, want created_at", wmeta.ColumnFor("CreatedAt"))
	}
	if wmeta.ColumnFor("UpdatedAt") != "updated_at" {
		t.Errorf("managedWidget UpdatedAt column = %q, want updated_at", wmeta.ColumnFor("UpdatedAt"))
	}
}

// TestTimestampsOptOut_CreateMap inserts via the map path. Success against a
// table with no created_at/updated_at proves neither column is referenced.
func TestTimestampsOptOut_CreateMap(t *testing.T) {
	m := setupOptOutTest(t)
	ctx := context.Background()

	rel, err := orm.Model[catalogRelease]{}.Create(ctx, map[string]any{"version": "v1.0.0"})
	if err != nil {
		t.Fatalf("Create(map): %v", err)
	}
	if rel.Version != "v1.0.0" || rel.ID == 0 {
		t.Errorf("unexpected row: %+v", rel)
	}
	// No timestamp stamped in memory for a column that does not exist.
	if !rel.CreatedAt.IsZero() || !rel.UpdatedAt.IsZero() {
		t.Errorf("timestamps should stay zero, got created=%v updated=%v", rel.CreatedAt, rel.UpdatedAt)
	}
	ormtesting.AssertDatabaseHas(t, m, "catalog_releases", map[string]any{"version": "v1.0.0"})
	ormtesting.AssertDatabaseCount(t, m, "catalog_releases", 1)
}

// TestTimestampsOptOut_CreateStruct inserts via the *T path.
func TestTimestampsOptOut_CreateStruct(t *testing.T) {
	m := setupOptOutTest(t)
	ctx := context.Background()

	rel, err := orm.Model[catalogRelease]{}.Create(ctx, &catalogRelease{Version: "v2.0.0"})
	if err != nil {
		t.Fatalf("Create(*T): %v", err)
	}
	if rel.ID == 0 {
		t.Error("expected non-zero id")
	}
	if !rel.CreatedAt.IsZero() || !rel.UpdatedAt.IsZero() {
		t.Errorf("timestamps should stay zero, got created=%v updated=%v", rel.CreatedAt, rel.UpdatedAt)
	}
	ormtesting.AssertDatabaseHas(t, m, "catalog_releases", map[string]any{"version": "v2.0.0"})
}

// TestTimestampsOptOut_UpdateInstance updates an existing row through Save.
func TestTimestampsOptOut_UpdateInstance(t *testing.T) {
	m := setupOptOutTest(t)
	ctx := context.Background()

	rel, err := orm.Model[catalogRelease]{}.Create(ctx, &catalogRelease{Version: "v1"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	rel.Version = "v1.1"
	if err := orm.Save(ctx, nil, rel); err != nil {
		t.Fatalf("Save (update): %v", err)
	}
	ormtesting.AssertDatabaseHas(t, m, "catalog_releases", map[string]any{"id": rel.ID, "version": "v1.1"})
	ormtesting.AssertDatabaseMissing(t, m, "catalog_releases", map[string]any{"version": "v1"})
}

// TestTimestampsOptOut_UpdateBulk updates through the map-based bulk path,
// which would inject updated_at if the model still managed timestamps.
func TestTimestampsOptOut_UpdateBulk(t *testing.T) {
	m := setupOptOutTest(t)
	ctx := context.Background()

	rel, err := orm.Model[catalogRelease]{}.Create(ctx, &catalogRelease{Version: "a"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	n, err := orm.Model[catalogRelease]{}.Update(ctx,
		map[string]any{"id": rel.ID},
		map[string]any{"version": "b"},
	)
	if err != nil {
		t.Fatalf("bulk Update: %v", err)
	}
	if n != 1 {
		t.Errorf("affected rows = %d, want 1", n)
	}
	ormtesting.AssertDatabaseHas(t, m, "catalog_releases", map[string]any{"id": rel.ID, "version": "b"})
}

// TestTimestampsOptOut_Queryable exercises the read helpers.
func TestTimestampsOptOut_Queryable(t *testing.T) {
	setupOptOutTest(t)
	ctx := context.Background()

	for _, v := range []string{"x", "y", "z"} {
		_, err := orm.Model[catalogRelease]{}.Create(ctx, &catalogRelease{Version: v})
		if err != nil {
			t.Fatalf("seed %q: %v", v, err)
		}
	}

	list, err := orm.Model[catalogRelease]{}.Where("version = ?", "y").Get(ctx)
	if err != nil {
		t.Fatalf("Where().Get: %v", err)
	}
	if len(list) != 1 || list[0].Version != "y" {
		t.Fatalf("Where().Get = %+v, want one row 'y'", list)
	}

	first, err := orm.Model[catalogRelease]{}.First(ctx)
	if err != nil {
		t.Fatalf("First: %v", err)
	}
	if first.ID == 0 {
		t.Error("First returned zero id")
	}

	found, err := orm.Model[catalogRelease]{}.Find(ctx, first.ID)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if found.ID != first.ID {
		t.Errorf("Find id = %d, want %d", found.ID, first.ID)
	}
}

// TestTimestampsOptOut_UUIDLast proves UUID Last() does not reference
// created_at for a timestamps-opted-out model (the table has no such column),
// for both the plain UUID and soft-delete UUID bases.
func TestTimestampsOptOut_UUIDLast(t *testing.T) {
	setupOptOutTest(t)
	ctx := context.Background()

	for _, v := range []string{"u1", "u2"} {
		if _, err := (orm.UUIDModel[catalogReleaseUUID]{}).Create(ctx, &catalogReleaseUUID{Version: v}); err != nil {
			t.Fatalf("seed uuid %q: %v", v, err)
		}
	}
	last, err := orm.UUIDModel[catalogReleaseUUID]{}.Last(ctx)
	if err != nil {
		t.Fatalf("UUID Last() on opted-out model: %v", err)
	}
	if last.Version == "" {
		t.Error("UUID Last() returned an empty row")
	}

	for _, v := range []string{"s1", "s2"} {
		if _, err := (orm.SoftDeleteUUIDModel[catalogReleaseSoftUUID]{}).Create(ctx, &catalogReleaseSoftUUID{Version: v}); err != nil {
			t.Fatalf("seed soft uuid %q: %v", v, err)
		}
	}
	softLast, err := orm.SoftDeleteUUIDModel[catalogReleaseSoftUUID]{}.Last(ctx)
	if err != nil {
		t.Fatalf("soft-delete UUID Last() on opted-out model: %v", err)
	}
	if softLast.Version == "" {
		t.Error("soft-delete UUID Last() returned an empty row")
	}
}

// TestTimestamps_UUIDLastStillOrdersByCreatedAt is the counter-test: a managed
// UUID model's Last() still returns the most-recently-created row by
// created_at (UUID ids are non-monotonic, so id ordering could not produce a
// deterministic result here).
func TestTimestamps_UUIDLastStillOrdersByCreatedAt(t *testing.T) {
	setupOptOutTest(t)
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	older := &managedReleaseUUID{Version: "older"}
	older.CreatedAt = base
	newer := &managedReleaseUUID{Version: "newer"}
	newer.CreatedAt = base.Add(time.Hour)

	if _, err := (orm.UUIDModel[managedReleaseUUID]{}).Create(ctx, older); err != nil {
		t.Fatalf("seed older: %v", err)
	}
	if _, err := (orm.UUIDModel[managedReleaseUUID]{}).Create(ctx, newer); err != nil {
		t.Fatalf("seed newer: %v", err)
	}

	last, err := orm.UUIDModel[managedReleaseUUID]{}.Last(ctx)
	if err != nil {
		t.Fatalf("managed UUID Last(): %v", err)
	}
	if last.Version != "newer" {
		t.Errorf("managed UUID Last() = %q, want \"newer\" (ordered by created_at)", last.Version)
	}
}

// TestTimestamps_DefaultStillManaged is the regression counter-test: a normal
// Model[T] without the opt-out still has its timestamps stamped on insert and
// re-stamped (never backwards) on update.
func TestTimestamps_DefaultStillManaged(t *testing.T) {
	m := setupOptOutTest(t)
	ctx := context.Background()

	w, err := orm.Model[managedWidget]{}.Create(ctx, &managedWidget{Name: "gear"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if w.CreatedAt.IsZero() || w.UpdatedAt.IsZero() {
		t.Errorf("timestamps should be stamped on insert, got created=%v updated=%v", w.CreatedAt, w.UpdatedAt)
	}
	ormtesting.AssertDatabaseHas(t, m, "managed_widgets", map[string]any{"name": "gear"})

	createdAt := w.CreatedAt
	firstUpdated := w.UpdatedAt

	w.Name = "gear-2"
	if err := orm.Save(ctx, nil, w); err != nil {
		t.Fatalf("Save (update): %v", err)
	}
	if w.CreatedAt != createdAt {
		t.Errorf("CreatedAt changed on update: %v -> %v", createdAt, w.CreatedAt)
	}
	if w.UpdatedAt.Before(firstUpdated) {
		t.Errorf("UpdatedAt went backwards: %v -> %v", firstUpdated, w.UpdatedAt)
	}
	ormtesting.AssertDatabaseHas(t, m, "managed_widgets", map[string]any{"name": "gear-2"})
}
