package orm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestUUIDUser is a UUIDModel-backed model used in ctx-propagation tests.
type TestUUIDUser struct {
	UUIDModel[TestUUIDUser]
	Name string `orm:"column:name"`
}

func (TestUUIDUser) TableName() string { return "test_uuid_users" }

// TestSoftDeleteUser is a SoftDeleteModel-backed model used in ctx-propagation tests.
type TestSoftDeleteUser struct {
	SoftDeleteModel[TestSoftDeleteUser]
	Name string `orm:"column:name"`
}

func (TestSoftDeleteUser) TableName() string { return "test_soft_users" }

// TestSoftDeleteUUIDUser is a SoftDeleteUUIDModel-backed model used in ctx-propagation tests.
type TestSoftDeleteUUIDUser struct {
	SoftDeleteUUIDModel[TestSoftDeleteUUIDUser]
	Name string `orm:"column:name"`
}

func (TestSoftDeleteUUIDUser) TableName() string { return "test_soft_uuid_users" }

// TestModel_CtxFirst_PropagatesToDriver asserts that the ctx passed
// to a read terminal flows through to the driver. We pass an
// already-cancelled context and confirm the driver returns the
// cancellation error. This replaces the old WithContext-based test:
// after the API simplification, ctx flows ONLY via terminal-arg, never
// via a chain helper.
func TestModel_CtxFirst_PropagatesToDriver(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "Alice", "alice@example.com", 30)

	// Cancelled-before-use ctx: any QueryContext call must surface
	// context.Canceled (or a wrapping error containing it).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Model[TestUser]{}.All(ctx)
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// TestModel_CtxFirst_SoftDeleteScopeStillApplies guards against a
// regression where the ctx-first read entry point skips the
// soft-delete predicate. SoftDeleteModel rows with deleted_at set must
// remain hidden from a default-scoped query.
func TestModel_CtxFirst_SoftDeleteScopeStillApplies(t *testing.T) {
	setupConvenienceTests(t)
	m := Default()

	// Seed two soft-delete users; trash the second.
	if _, err := m.DB().Exec("CREATE TABLE test_soft_users (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := m.DB().Exec("INSERT INTO test_soft_users (name, created_at, updated_at) VALUES ('alive', datetime('now'), datetime('now'))"); err != nil {
		t.Fatalf("seed alive: %v", err)
	}
	if _, err := m.DB().Exec("INSERT INTO test_soft_users (name, created_at, updated_at, deleted_at) VALUES ('trashed', datetime('now'), datetime('now'), datetime('now'))"); err != nil {
		t.Fatalf("seed trashed: %v", err)
	}

	got, err := SoftDeleteModel[TestSoftDeleteUser]{}.All(context.Background())
	if err != nil {
		t.Fatalf("All returned error: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("default-scoped All returned %d rows, want 1 (soft-delete predicate dropped)", len(got))
	}
	if len(got) >= 1 && got[0].Name != "alive" {
		t.Errorf("got user %q, want 'alive'", got[0].Name)
	}
}

// TestModel_CtxFirst_StaticFormWorks asserts the static form Model[T]{}.Find(ctx, id)
// works as the chain entry point.
func TestModel_CtxFirst_StaticFormWorks(t *testing.T) {
	setupConvenienceTests(t)
	id := seedUser(t, Default(), "Carol", "carol@example.com", 28)

	user, err := Model[TestUser]{}.Find(context.Background(), id)
	if err != nil {
		t.Fatalf("static Find returned error: %v", err)
	}
	if user.Name != "Carol" {
		t.Errorf("got name %q, want Carol", user.Name)
	}
}

// TestUUIDModel_CtxFirst_PropagatesToDriver mirrors the
// PropagatesToDriver test for the UUID variant. We seed via raw SQL so
// the test does not depend on Save (which would itself need a
// ctx-first path; out of scope for this read-side test).
func TestUUIDModel_CtxFirst_PropagatesToDriver(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())
	SetDefault(manager)
	defer ResetDefault()

	if _, err := manager.DB().Exec(`CREATE TABLE test_uuid_users (
		id TEXT PRIMARY KEY,
		name TEXT,
		created_at DATETIME,
		updated_at DATETIME
	)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	id := uuid.New().String()
	if _, err := manager.DB().Exec(
		"INSERT INTO test_uuid_users (id, name, created_at, updated_at) VALUES (?, ?, ?, ?)",
		id, "Bob", time.Now(), time.Now(),
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := UUIDModel[TestUUIDUser]{}.All(ctx)
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}
