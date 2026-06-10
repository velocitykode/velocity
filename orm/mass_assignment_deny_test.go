package orm

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// undeclaredUser declares neither Fillable() nor Guarded() nor
// AllowAllColumns: the deny-by-default target. Field names are chosen to
// look like the classic privilege-escalation payload.
type undeclaredUser struct {
	Model[undeclaredUser]
	Name    string `orm:"column:name"`
	Role    string `orm:"column:role"`
	IsAdmin bool   `orm:"column:is_admin"`
}

func (undeclaredUser) TableName() string { return "undeclared_users" }

// setupDenyDefaultTests creates the shared tables for the deny-by-default
// end-to-end tests and installs the default manager.
func setupDenyDefaultTests(t *testing.T) *Manager {
	t.Helper()
	manager := newTestManager(t)
	db := manager.DB()
	for _, ddl := range []string{
		`CREATE TABLE undeclared_users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT,
			role TEXT,
			is_admin BOOLEAN,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME
		)`,
		`CREATE TABLE fillable_models (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT,
			role TEXT,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME
		)`,
		`CREATE TABLE guarded_models (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT,
			role TEXT,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME
		)`,
		`CREATE TABLE open_policy_models (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT,
			is_admin BOOLEAN,
			role TEXT,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME
		)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	SetDefault(manager)
	t.Cleanup(func() {
		ResetDefault()
		manager.Shutdown(context.Background())
	})
	return manager
}

// requireMassAssignmentError asserts err is a *MassAssignmentError naming
// the model and every expected key.
func requireMassAssignmentError(t *testing.T, err error, model string, keys ...string) *MassAssignmentError {
	t.Helper()
	if err == nil {
		t.Fatal("expected *MassAssignmentError, got nil")
	}
	var mae *MassAssignmentError
	if !errors.As(err, &mae) {
		t.Fatalf("expected *MassAssignmentError, got %T: %v", err, err)
	}
	if mae.Model != model {
		t.Errorf("MassAssignmentError.Model = %q, want %q", mae.Model, model)
	}
	got := strings.Join(mae.Keys, ",")
	for _, k := range keys {
		found := false
		for _, have := range mae.Keys {
			if have == k {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("MassAssignmentError.Keys = [%s], missing %q", got, k)
		}
	}
	return mae
}

func TestCreateMap_UndeclaredModelRejected(t *testing.T) {
	manager := setupDenyDefaultTests(t)

	_, err := Model[undeclaredUser]{}.Create(context.Background(), map[string]any{
		"name":     "mallory",
		"is_admin": true,
	})
	requireMassAssignmentError(t, err, "orm.undeclaredUser", "name", "is_admin")

	var count int
	if scanErr := manager.DB().QueryRow("SELECT COUNT(*) FROM undeclared_users").Scan(&count); scanErr != nil {
		t.Fatalf("count: %v", scanErr)
	}
	if count != 0 {
		t.Errorf("rejected Create must not insert a row; found %d", count)
	}
}

func TestQueryCreateMap_UndeclaredModelRejected(t *testing.T) {
	setupDenyDefaultTests(t)

	_, err := Model[undeclaredUser]{}.Where("name = ?", "anyone").Create(context.Background(), map[string]any{
		"role": "admin",
	})
	requireMassAssignmentError(t, err, "orm.undeclaredUser", "role")
}

// TestCreateMap_UndeclaredModelRejectsCaseVariantKeys: mapToStruct's
// implicit-deny preflight matches keys case-insensitively (via
// deniedUpdateKeys), so "IS_ADMIN" is rejected like "is_admin" instead of
// being treated as resolving to no application column.
func TestCreateMap_UndeclaredModelRejectsCaseVariantKeys(t *testing.T) {
	setupDenyDefaultTests(t)

	_, err := Model[undeclaredUser]{}.Create(context.Background(), map[string]any{
		"IS_ADMIN": true,
	})
	requireMassAssignmentError(t, err, "orm.undeclaredUser", "IS_ADMIN")
}

// TestCreateMap_UndeclaredModelRejectsColumnAliasKeys pins that the
// snake-cased field name is denied alongside the db-tagged column name
// (undeclaredAliasedUser maps IsAdmin to column admin_flag). Rejection
// fires in mapToStruct before any write, so no table is needed.
func TestCreateMap_UndeclaredModelRejectsColumnAliasKeys(t *testing.T) {
	setupDenyDefaultTests(t)

	for _, key := range []string{"admin_flag", "is_admin", "ADMIN_FLAG"} {
		_, err := Model[undeclaredAliasedUser]{}.Create(context.Background(), map[string]any{
			key: true,
		})
		requireMassAssignmentError(t, err, "orm.undeclaredAliasedUser", key)
	}
}

func TestFirstOrCreate_UndeclaredModelRejected(t *testing.T) {
	setupDenyDefaultTests(t)

	_, err := Model[undeclaredUser]{}.FirstOrCreate(context.Background(),
		map[string]any{"name": "mallory"},
		map[string]any{"is_admin": true},
	)
	requireMassAssignmentError(t, err, "orm.undeclaredUser", "name", "is_admin")
}

// TestFirstOrCreate_UndeclaredModelRejected_HitBranch pins that the lookup
// conditions map is policed even when a matching row exists: before the
// fix, a hit returned the row immediately and the conditions keys were
// never checked, so the call succeeded silently on a no-policy model.
func TestFirstOrCreate_UndeclaredModelRejected_HitBranch(t *testing.T) {
	manager := setupDenyDefaultTests(t)

	if _, err := manager.DB().Exec(
		"INSERT INTO undeclared_users (name, role, is_admin, created_at, updated_at) VALUES ('alice', 'user', 0, datetime('now'), datetime('now'))",
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := Model[undeclaredUser]{}.FirstOrCreate(context.Background(),
		map[string]any{"name": "alice"},
		map[string]any{},
	)
	requireMassAssignmentError(t, err, "orm.undeclaredUser", "name")
}

// TestUpdateOrCreate_UndeclaredModelRejected_HitBranchConditionsOnly pins
// that conditions keys are rejected on the hit branch even when the values
// map carries no application column: before the fix, the hit branch only
// ran mapToStruct(values), so application-column keys in conditions
// slipped through unchecked.
func TestUpdateOrCreate_UndeclaredModelRejected_HitBranchConditionsOnly(t *testing.T) {
	manager := setupDenyDefaultTests(t)

	if _, err := manager.DB().Exec(
		"INSERT INTO undeclared_users (name, role, is_admin, created_at, updated_at) VALUES ('alice', 'user', 0, datetime('now'), datetime('now'))",
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := Model[undeclaredUser]{}.UpdateOrCreate(context.Background(),
		map[string]any{"name": "alice"},
		map[string]any{},
	)
	requireMassAssignmentError(t, err, "orm.undeclaredUser", "name")

	var name string
	if scanErr := manager.DB().QueryRow("SELECT name FROM undeclared_users WHERE id = 1").Scan(&name); scanErr != nil {
		t.Fatalf("scan: %v", scanErr)
	}
	if name != "alice" {
		t.Errorf("rejected UpdateOrCreate must not write; name = %q", name)
	}
}

func TestUpdateOrCreate_UndeclaredModelRejected_BothBranches(t *testing.T) {
	manager := setupDenyDefaultTests(t)

	// Miss branch: no row matches, the merged map is rejected on create.
	_, err := Model[undeclaredUser]{}.UpdateOrCreate(context.Background(),
		map[string]any{"name": "mallory"},
		map[string]any{"is_admin": true},
	)
	requireMassAssignmentError(t, err, "orm.undeclaredUser", "name", "is_admin")

	// Hit branch: seed a row directly, then the values map is rejected on
	// the update path before anything is written.
	if _, seedErr := manager.DB().Exec(
		"INSERT INTO undeclared_users (name, role, is_admin, created_at, updated_at) VALUES ('alice', 'user', 0, datetime('now'), datetime('now'))",
	); seedErr != nil {
		t.Fatalf("seed: %v", seedErr)
	}
	_, err = Model[undeclaredUser]{}.UpdateOrCreate(context.Background(),
		map[string]any{"name": "alice"},
		map[string]any{"is_admin": true},
	)
	requireMassAssignmentError(t, err, "orm.undeclaredUser", "is_admin")

	var isAdmin bool
	if scanErr := manager.DB().QueryRow("SELECT is_admin FROM undeclared_users WHERE name = 'alice'").Scan(&isAdmin); scanErr != nil {
		t.Fatalf("scan: %v", scanErr)
	}
	if isAdmin {
		t.Error("rejected UpdateOrCreate must not persist is_admin")
	}
}

// TestFirstOrCreate_UndeclaredModelRejectsCaseVariantKeys: conditions and
// values keys become SQL identifiers (lookup WHERE / insert columns), where
// most dialects fold unquoted identifier case, so "IS_ADMIN" must be
// rejected like "is_admin" in either map.
func TestFirstOrCreate_UndeclaredModelRejectsCaseVariantKeys(t *testing.T) {
	setupDenyDefaultTests(t)

	_, err := Model[undeclaredUser]{}.FirstOrCreate(context.Background(),
		map[string]any{"NAME": "mallory"},
		map[string]any{"IS_ADMIN": true},
	)
	requireMassAssignmentError(t, err, "orm.undeclaredUser", "NAME", "IS_ADMIN")
}

func TestUpdateOrCreate_UndeclaredModelRejectsCaseVariantKeys(t *testing.T) {
	setupDenyDefaultTests(t)

	_, err := Model[undeclaredUser]{}.UpdateOrCreate(context.Background(),
		map[string]any{"NAME": "mallory"},
		map[string]any{"IS_ADMIN": true},
	)
	requireMassAssignmentError(t, err, "orm.undeclaredUser", "NAME", "IS_ADMIN")
}

// TestFirstOrCreate_UndeclaredModelRejectsColumnAliasKeys pins that the
// snake-cased field name is denied alongside the db-tagged column name
// (undeclaredAliasedUser maps IsAdmin to column admin_flag), in both the
// conditions and values maps. Rejection fires before the lookup query, so
// no table is needed.
func TestFirstOrCreate_UndeclaredModelRejectsColumnAliasKeys(t *testing.T) {
	setupDenyDefaultTests(t)

	for _, key := range []string{"admin_flag", "is_admin", "ADMIN_FLAG"} {
		_, err := Model[undeclaredAliasedUser]{}.FirstOrCreate(context.Background(),
			map[string]any{key: true},
			map[string]any{},
		)
		requireMassAssignmentError(t, err, "orm.undeclaredAliasedUser", key)

		_, err = Model[undeclaredAliasedUser]{}.FirstOrCreate(context.Background(),
			map[string]any{},
			map[string]any{key: true},
		)
		requireMassAssignmentError(t, err, "orm.undeclaredAliasedUser", key)
	}
}

func TestUpdateOrCreate_UndeclaredModelRejectsColumnAliasKeys(t *testing.T) {
	setupDenyDefaultTests(t)

	for _, key := range []string{"admin_flag", "is_admin", "ADMIN_FLAG"} {
		_, err := Model[undeclaredAliasedUser]{}.UpdateOrCreate(context.Background(),
			map[string]any{key: true},
			map[string]any{},
		)
		requireMassAssignmentError(t, err, "orm.undeclaredAliasedUser", key)

		_, err = Model[undeclaredAliasedUser]{}.UpdateOrCreate(context.Background(),
			map[string]any{},
			map[string]any{key: true},
		)
		requireMassAssignmentError(t, err, "orm.undeclaredAliasedUser", key)
	}
}

func TestCreateMap_FillableModelAcceptsListedSkipsUnlisted(t *testing.T) {
	setupDenyDefaultTests(t)

	// fillableModel (fillable_test.go) allows only "name".
	created, err := Model[fillableModel]{}.Create(context.Background(), map[string]any{
		"name": "alice",
		"role": "admin",
	})
	if err != nil {
		t.Fatalf("Create on Fillable model: %v", err)
	}
	if created.Name != "alice" {
		t.Errorf("listed key should be written; Name = %q", created.Name)
	}
	if created.Role != "" {
		t.Errorf("unlisted key must be skipped; Role = %q", created.Role)
	}
}

func TestCreateMap_GuardedModelAcceptsUnguardedSkipsGuarded(t *testing.T) {
	setupDenyDefaultTests(t)

	// guardedModel (fillable_test.go) guards only "role".
	created, err := Model[guardedModel]{}.Create(context.Background(), map[string]any{
		"name": "bob",
		"role": "admin",
	})
	if err != nil {
		t.Fatalf("Create on Guarded model: %v", err)
	}
	if created.Name != "bob" {
		t.Errorf("unguarded key should be written; Name = %q", created.Name)
	}
	if created.Role != "" {
		t.Errorf("guarded key must be skipped; Role = %q", created.Role)
	}
}

func TestCreateMap_AllowAllColumnsRestoresOpenBehavior(t *testing.T) {
	setupDenyDefaultTests(t)

	created, err := Model[openPolicyModel]{}.Create(context.Background(), map[string]any{
		"name":     "carol",
		"is_admin": true,
		"role":     "admin",
	})
	if err != nil {
		t.Fatalf("Create on AllowAllColumns model: %v", err)
	}
	if created.Name != "carol" || !created.IsAdmin || created.Role != "admin" {
		t.Errorf("AllowAllColumns model should accept every column; got %+v", created)
	}
}

// TestCreateStruct_UndeclaredModelUnaffected pins the audit's scoping: the
// deny-by-default flip governs map-based writes only. A *T the developer
// constructed in code persists all fields even with no policy declared.
func TestCreateStruct_UndeclaredModelUnaffected(t *testing.T) {
	setupDenyDefaultTests(t)

	created, err := Model[undeclaredUser]{}.Create(context.Background(), &undeclaredUser{
		Name:    "dave",
		Role:    "admin",
		IsAdmin: true,
	})
	if err != nil {
		t.Fatalf("Create(*T) on undeclared model: %v", err)
	}
	if created.Name != "dave" || created.Role != "admin" || !created.IsAdmin {
		t.Errorf("struct-based Create must persist caller-set fields unchanged; got %+v", created)
	}

	found, err := Model[undeclaredUser]{}.Find(context.Background(), int64(created.ID))
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if found.Role != "admin" || !found.IsAdmin {
		t.Errorf("persisted row should keep caller-set fields; got %+v", found)
	}
}

// TestMapToStruct_RejectsBeforeWriting verifies the error fires before any
// field is hydrated, so callers never observe a partially filled model.
func TestMapToStruct_RejectsBeforeWriting(t *testing.T) {
	var dst undeclaredUser
	err := mapToStruct(map[string]any{"name": "x", "role": "admin"}, &dst)
	requireMassAssignmentError(t, err, "orm.undeclaredUser", "name", "role")
	if dst.Name != "" || dst.Role != "" {
		t.Errorf("model must stay zero on rejection; got %+v", dst)
	}
}

// TestMapToStruct_UnknownAndEmbeddedKeysDoNotTriggerRejection: keys that
// resolve to no column are ignored (as before), and framework-managed
// embedded columns bypass policy, so a map carrying only those proceeds.
func TestMapToStruct_UnknownAndEmbeddedKeysDoNotTriggerRejection(t *testing.T) {
	var dst undeclaredUser
	if err := mapToStruct(map[string]any{"garbage": 1, "id": uint(7)}, &dst); err != nil {
		t.Fatalf("expected no error for unknown + embedded keys, got %v", err)
	}
	if dst.ID != 7 {
		t.Errorf("embedded id should still hydrate; got %d", dst.ID)
	}
}

func TestModelUpdate_UndeclaredModelRejected(t *testing.T) {
	manager := setupDenyDefaultTests(t)

	if _, err := manager.DB().Exec(
		"INSERT INTO undeclared_users (name, role, is_admin, created_at, updated_at) VALUES ('alice', 'user', 0, datetime('now'), datetime('now'))",
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := Model[undeclaredUser]{}.Update(context.Background(),
		map[string]any{"name": "alice"},
		map[string]any{"is_admin": true, "role": "admin"},
	)
	requireMassAssignmentError(t, err, "orm.undeclaredUser", "is_admin", "role")

	var role string
	var isAdmin bool
	if scanErr := manager.DB().QueryRow("SELECT role, is_admin FROM undeclared_users WHERE name = 'alice'").Scan(&role, &isAdmin); scanErr != nil {
		t.Fatalf("scan: %v", scanErr)
	}
	if role != "user" || isAdmin {
		t.Errorf("rejected Update must not write; role = %q, is_admin = %v", role, isAdmin)
	}
}

func TestQueryUpdate_UndeclaredModelRejected(t *testing.T) {
	setupDenyDefaultTests(t)

	_, err := Model[undeclaredUser]{}.Where("name = ?", "anyone").Update(context.Background(), map[string]any{
		"role": "admin",
	})
	requireMassAssignmentError(t, err, "orm.undeclaredUser", "role")
}

// TestQueryUpdate_UndeclaredModelRejectsCaseVariantKeys: a bulk Update
// compiles map keys directly into SQL, where most dialects fold unquoted
// identifier case, so "IS_ADMIN" must be rejected like "is_admin".
func TestQueryUpdate_UndeclaredModelRejectsCaseVariantKeys(t *testing.T) {
	setupDenyDefaultTests(t)

	_, err := Model[undeclaredUser]{}.Where("name = ?", "anyone").Update(context.Background(), map[string]any{
		"IS_ADMIN": true,
	})
	requireMassAssignmentError(t, err, "orm.undeclaredUser", "IS_ADMIN")
}

// undeclaredAliasedUser maps IsAdmin to a column whose name differs from
// the snake-cased field name, pinning that neither form bypasses the
// implicit-deny check on bulk Update.
type undeclaredAliasedUser struct {
	Model[undeclaredAliasedUser]
	IsAdmin bool `orm:"column:admin_flag"`
}

func (undeclaredAliasedUser) TableName() string { return "undeclared_aliased_users" }

func TestQueryUpdate_UndeclaredModelRejectsColumnAliasKeys(t *testing.T) {
	setupDenyDefaultTests(t)

	// Rejection fires before SQL compilation, so no table is needed.
	for _, key := range []string{"admin_flag", "is_admin", "ADMIN_FLAG"} {
		_, err := Model[undeclaredAliasedUser]{}.Where("id = ?", 1).Update(context.Background(), map[string]any{
			key: true,
		})
		requireMassAssignmentError(t, err, "orm.undeclaredAliasedUser", key)
	}
}

// TestQueryUpdate_DeclaredAndOpenPolicyModelsUnaffected pins that the
// implicit-deny check on bulk Update does not touch models with a
// declared Fillable/Guarded policy or an AllowAllColumns opt-in.
func TestQueryUpdate_DeclaredAndOpenPolicyModelsUnaffected(t *testing.T) {
	setupDenyDefaultTests(t)

	_, err := Model[fillableModel]{}.Where("name = ?", "nobody").Update(context.Background(), map[string]any{
		"name": "renamed",
	})
	if err != nil {
		t.Fatalf("Update on Fillable model: %v", err)
	}
	_, err = Model[guardedModel]{}.Where("name = ?", "nobody").Update(context.Background(), map[string]any{
		"name": "renamed",
	})
	if err != nil {
		t.Fatalf("Update on Guarded model: %v", err)
	}
	_, err = Model[openPolicyModel]{}.Where("name = ?", "nobody").Update(context.Background(), map[string]any{
		"is_admin": true,
	})
	if err != nil {
		t.Fatalf("Update on AllowAllColumns model: %v", err)
	}
}

// undeclaredSoftUser has no declared policy but is soft-deletable. Delete
// routes through bulkUpdate writing only the embedded deleted_at column,
// which bypasses policy by design, so soft delete must keep working on
// undeclared models.
type undeclaredSoftUser struct {
	SoftDeleteModel[undeclaredSoftUser]
	Name string `orm:"column:name"`
}

func (undeclaredSoftUser) TableName() string { return "undeclared_soft_users" }

func TestDelete_UndeclaredSoftDeleteModelStillSoftDeletes(t *testing.T) {
	manager := setupDenyDefaultTests(t)
	db := manager.DB()
	if _, err := db.Exec(`CREATE TABLE undeclared_soft_users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT,
		created_at DATETIME,
		updated_at DATETIME,
		deleted_at DATETIME
	)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec(
		"INSERT INTO undeclared_soft_users (name, created_at, updated_at) VALUES ('alice', datetime('now'), datetime('now'))",
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	affected, err := SoftDeleteModel[undeclaredSoftUser]{}.Where("name = ?", "alice").Delete(context.Background())
	if err != nil {
		t.Fatalf("soft Delete on undeclared model: %v", err)
	}
	if affected != 1 {
		t.Fatalf("affected = %d, want 1", affected)
	}

	var deletedAt sql.NullString
	if scanErr := db.QueryRow("SELECT deleted_at FROM undeclared_soft_users WHERE name = 'alice'").Scan(&deletedAt); scanErr != nil {
		t.Fatalf("scan: %v", scanErr)
	}
	if !deletedAt.Valid {
		t.Error("deleted_at should be stamped by soft delete")
	}
}
