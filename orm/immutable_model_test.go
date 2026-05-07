package orm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// AuditLog is an append-only ImmutableModel-backed model used to exercise
// the ImmutableModel[T] path. It mirrors the canonical "audit_logs" use
// case (no updated_at column) called out by item 16.
type AuditLog struct {
	ImmutableModel[AuditLog]
	Action  string `orm:"column:action"`
	Subject string `orm:"column:subject"`
}

func (AuditLog) TableName() string { return "audit_logs" }

// AuditLogUUID is the UUID-keyed counterpart for ImmutableUUIDModel
// coverage.
type AuditLogUUID struct {
	ImmutableUUIDModel[AuditLogUUID]
	Action string `orm:"column:action"`
}

func (AuditLogUUID) TableName() string { return "audit_log_uuids" }

// setupImmutableTests creates the audit_logs table without an updated_at
// column so any spurious ORM-side stamping would fail at the driver.
func setupImmutableTests(t *testing.T) *Manager {
	t.Helper()
	manager := newTestManager(t)
	if _, err := manager.DB().Exec(`CREATE TABLE audit_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		action TEXT NOT NULL,
		subject TEXT NOT NULL,
		created_at DATETIME
	)`); err != nil {
		t.Fatalf("create audit_logs: %v", err)
	}
	if _, err := manager.DB().Exec(`CREATE TABLE audit_log_uuids (
		id TEXT PRIMARY KEY,
		action TEXT NOT NULL,
		created_at DATETIME
	)`); err != nil {
		t.Fatalf("create audit_log_uuids: %v", err)
	}
	SetDefault(manager)
	t.Cleanup(func() {
		ResetDefault()
		manager.Shutdown(context.Background())
	})
	return manager
}

// TestImmutableModel_CreateAndRead verifies the happy path: inserting a
// row through Save(nil, &record) and reading it back.
func TestImmutableModel_CreateAndRead(t *testing.T) {
	setupImmutableTests(t)

	rec := &AuditLog{Action: "user.login", Subject: "alice@example.com"}
	if err := Save(nil, rec); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	if rec.ID == 0 {
		t.Error("ID was not populated after insert")
	}
	if !rec.IsExisting {
		t.Error("IsExisting was not set to true after insert")
	}

	got, err := Model[AuditLog]{}.Find(int(rec.ID))
	if err != nil {
		t.Fatalf("Find returned error: %v", err)
	}
	if got.Action != "user.login" {
		t.Errorf("got Action %q, want user.login", got.Action)
	}
}

// TestImmutableModel_StaticHelpers verifies the static helpers attached
// to ImmutableModel[T] resolve correctly: Where/All/Count/Pluck.
func TestImmutableModel_StaticHelpers(t *testing.T) {
	setupImmutableTests(t)

	for i, action := range []string{"a", "b", "c"} {
		rec := &AuditLog{Action: action, Subject: "subj"}
		if err := Save(nil, rec); err != nil {
			t.Fatalf("seed[%d]: %v", i, err)
		}
	}

	all, err := ImmutableModel[AuditLog]{}.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("All returned %d, want 3", len(all))
	}

	cnt, err := ImmutableModel[AuditLog]{}.Count()
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if cnt != 3 {
		t.Errorf("Count = %d, want 3", cnt)
	}

	actions, err := ImmutableModel[AuditLog]{}.Pluck("action")
	if err != nil {
		t.Fatalf("Pluck: %v", err)
	}
	if len(actions) != 3 {
		t.Errorf("Pluck returned %d, want 3", len(actions))
	}
}

// TestImmutableModel_SaveOnExistingFails asserts the instance method
// Save() returns ErrImmutableModelUpdate when invoked on a record
// already marked as existing. This is the runtime guard that mirrors
// the type-system inability to find an Update method.
func TestImmutableModel_SaveOnExistingFails(t *testing.T) {
	setupImmutableTests(t)

	rec := &AuditLog{Action: "a", Subject: "s"}
	if err := Save(nil, rec); err != nil {
		t.Fatalf("initial Save: %v", err)
	}
	// Record is now existing; second Save must reject.
	if err := Save(nil, rec); !errors.Is(err, ErrImmutableModelUpdate) {
		t.Errorf("second Save error = %v, want ErrImmutableModelUpdate", err)
	}

	// Calling .Save on the embedded base directly also rejects.
	if err := rec.ImmutableModel.Save(); !errors.Is(err, ErrImmutableModelUpdate) {
		t.Errorf("ImmutableModel.Save error = %v, want ErrImmutableModelUpdate", err)
	}
}

// TestImmutableModel_QueryUpdateSkipsUpdatedAt asserts the Query.Update
// path does not inject updated_at when the model has no UpdatedAt
// column. The pre-fix behaviour would silently emit
// "UPDATE audit_logs SET ... updated_at = NOW()" and fail at the
// driver because the column is missing.
func TestImmutableModel_QueryUpdateSkipsUpdatedAt(t *testing.T) {
	setupImmutableTests(t)

	rec := &AuditLog{Action: "a", Subject: "s"}
	if err := Save(nil, rec); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Query.Update on an immutable model should skip the updated_at
	// injection. Update the action column; if injection were active
	// SQLite would error with "no such column: updated_at".
	q := newQuery[AuditLog]()
	affected, err := q.Where("id = ?", int(rec.ID)).Update(map[string]any{"action": "a-updated"})
	if err != nil {
		t.Fatalf("Update returned error (likely updated_at injection): %v", err)
	}
	if affected != 1 {
		t.Errorf("affected = %d, want 1", affected)
	}

	// Verify the SQL did not contain updated_at.
	sql, _ := q.ToSQL()
	if strings.Contains(strings.ToLower(sql), "updated_at") {
		t.Errorf("Update SQL injected updated_at on immutable model: %q", sql)
	}
}

// TestModel_QueryUpdateStillInjectsUpdatedAt is the inverse guard: a
// regular Model[T] still gets the updated_at injection (the new
// hasUpdatedAt detection must not regress that path).
func TestModel_QueryUpdateStillInjectsUpdatedAt(t *testing.T) {
	setupConvenienceTests(t)
	id := seedUser(t, Default(), "Alice", "alice@example.com", 30)

	q := newQuery[TestUser]()
	if _, err := q.Where("id = ?", id).Update(map[string]any{"name": "Alice2"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	sql, _ := q.ToSQL()
	if !strings.Contains(strings.ToLower(sql), "updated_at") {
		t.Errorf("Update SQL did not inject updated_at on Model[T]: %q", sql)
	}
}

// TestImmutableModel_WithContext verifies WithContext on ImmutableModel
// returns a *Query[T] bound to ctx (matches the Item 3 wiring).
func TestImmutableModel_WithContext(t *testing.T) {
	type ctxKey string
	const k ctxKey = "tracer"
	ctx := context.WithValue(context.Background(), k, "v")

	q := ImmutableModel[AuditLog]{}.WithContext(ctx)
	if q == nil || q.ctx == nil {
		t.Fatal("WithContext returned nil or unbound ctx")
	}
	if got := q.ctx.Value(k); got != "v" {
		t.Errorf("ctx not propagated: got %v", got)
	}
}

// TestImmutableUUIDModel_CreateAndRead exercises the UUID-keyed variant.
func TestImmutableUUIDModel_CreateAndRead(t *testing.T) {
	setupImmutableTests(t)

	rec := &AuditLogUUID{Action: "uuid-test"}
	if err := Save(nil, rec); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if rec.ID == "" {
		t.Error("UUID ID was not populated after insert")
	}

	got, err := ImmutableUUIDModel[AuditLogUUID]{}.Find(rec.ID)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got.Action != "uuid-test" {
		t.Errorf("got Action %q", got.Action)
	}

	// The Find result must round-trip IsExisting so a re-Save hits
	// the immutable-update guard loudly instead of silently producing
	// a duplicate (auto-inc) or a raw DB unique-key error (UUID).
	if !got.IsExisting {
		t.Fatal("Find did not mark IsExisting on Immutable variant")
	}
	if err := Save(nil, got); !errors.Is(err, ErrImmutableModelUpdate) {
		t.Errorf("re-Save via Find result error = %v, want ErrImmutableModelUpdate", err)
	}
	// The freshly-Created pointer must also fail the same way; it has
	// IsExisting=true set by saveImmutableUUIDModel after insert.
	if err := Save(nil, rec); !errors.Is(err, ErrImmutableModelUpdate) {
		t.Errorf("re-Save via Created pointer error = %v, want ErrImmutableModelUpdate", err)
	}
}

// TestImmutableModel_NoUpdateMethod_CompileGuard documents (via comment)
// that ImmutableModel[T] / ImmutableUUIDModel[T] do not declare a
// static-form Update method. There is no runtime assertion possible:
// type-system absence is enforced by the compiler. The guard is the
// presence of the explicit instance-Save error path covered above plus
// the absence of an Update method receiver in immutable_model.go.
//
// If an Update method ever sneaks back in, the change diff itself is
// the signal; this test is a noop kept for documentation.
func TestImmutableModel_NoUpdateMethod_CompileGuard(t *testing.T) {
	// No assertion; documentation only. See comment.
}

// TestImmutableModel_RespectsCallerCreatedAt verifies that the
// auto-increment ImmutableModel save path does not clobber a caller-set
// CreatedAt and stamps it when zero.
func TestImmutableModel_RespectsCallerCreatedAt(t *testing.T) {
	manager := setupImmutableTests(t)
	db := manager.DB()

	preset := time.Date(2019, 11, 5, 9, 30, 0, 0, time.UTC)
	r1 := &AuditLog{Action: "user.import", Subject: "legacy"}
	r1.CreatedAt = preset
	if err := Save(nil, r1); err != nil {
		t.Fatalf("Save with preset CreatedAt: %v", err)
	}
	if !r1.CreatedAt.Equal(preset) {
		t.Errorf("in-memory CreatedAt was clobbered: got %v, want %v", r1.CreatedAt, preset)
	}
	// Re-read from DB to defeat any serialization-layer bug.
	var dbCreated time.Time
	if err := db.QueryRow("SELECT created_at FROM audit_logs WHERE id = ?", r1.ID).Scan(&dbCreated); err != nil {
		t.Fatalf("read back r1 row: %v", err)
	}
	if !dbCreated.Equal(preset) {
		t.Errorf("persisted created_at != preset: got %v, want %v", dbCreated, preset)
	}

	r2 := &AuditLog{Action: "user.create", Subject: "alice"}
	before := time.Now()
	if err := Save(nil, r2); err != nil {
		t.Fatalf("Save with zero CreatedAt: %v", err)
	}
	after := time.Now()
	if r2.CreatedAt.IsZero() {
		t.Fatal("Expected in-memory CreatedAt to be auto-stamped")
	}
	if r2.CreatedAt.Before(before.Add(-time.Second)) || r2.CreatedAt.After(after.Add(time.Second)) {
		t.Errorf("in-memory CreatedAt %v not within 1s of now [%v, %v]", r2.CreatedAt, before, after)
	}
	if err := db.QueryRow("SELECT created_at FROM audit_logs WHERE id = ?", r2.ID).Scan(&dbCreated); err != nil {
		t.Fatalf("read back r2 row: %v", err)
	}
	if dbCreated.Before(before.Add(-2*time.Second)) || dbCreated.After(after.Add(2*time.Second)) {
		t.Errorf("persisted created_at %v not within 2s of now [%v, %v]", dbCreated, before, after)
	}
}

// TestImmutableUUIDModel_RespectsCallerCreatedAt mirrors the above for
// the UUID-keyed immutable save path.
func TestImmutableUUIDModel_RespectsCallerCreatedAt(t *testing.T) {
	manager := setupImmutableTests(t)
	db := manager.DB()

	preset := time.Date(2018, 4, 1, 0, 0, 0, 0, time.UTC)
	r1 := &AuditLogUUID{Action: "system.boot"}
	r1.CreatedAt = preset
	if err := Save(nil, r1); err != nil {
		t.Fatalf("Save with preset CreatedAt: %v", err)
	}
	if !r1.CreatedAt.Equal(preset) {
		t.Errorf("in-memory CreatedAt was clobbered: got %v, want %v", r1.CreatedAt, preset)
	}
	// Re-read from DB to defeat any serialization-layer bug.
	var dbCreated time.Time
	if err := db.QueryRow("SELECT created_at FROM audit_log_uuids WHERE id = ?", r1.ID).Scan(&dbCreated); err != nil {
		t.Fatalf("read back r1 row: %v", err)
	}
	if !dbCreated.Equal(preset) {
		t.Errorf("persisted created_at != preset: got %v, want %v", dbCreated, preset)
	}

	r2 := &AuditLogUUID{Action: "system.shutdown"}
	before := time.Now()
	if err := Save(nil, r2); err != nil {
		t.Fatalf("Save with zero CreatedAt: %v", err)
	}
	after := time.Now()
	if r2.CreatedAt.IsZero() {
		t.Fatal("Expected in-memory CreatedAt to be auto-stamped")
	}
	if r2.CreatedAt.Before(before.Add(-time.Second)) || r2.CreatedAt.After(after.Add(time.Second)) {
		t.Errorf("in-memory CreatedAt %v not within 1s of now [%v, %v]", r2.CreatedAt, before, after)
	}
	if err := db.QueryRow("SELECT created_at FROM audit_log_uuids WHERE id = ?", r2.ID).Scan(&dbCreated); err != nil {
		t.Fatalf("read back r2 row: %v", err)
	}
	if dbCreated.Before(before.Add(-2*time.Second)) || dbCreated.After(after.Add(2*time.Second)) {
		t.Errorf("persisted created_at %v not within 2s of now [%v, %v]", dbCreated, before, after)
	}
}
