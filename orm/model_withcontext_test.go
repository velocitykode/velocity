package orm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestUUIDUser is a UUIDModel-backed model used in WithContext tests.
type TestUUIDUser struct {
	UUIDModel[TestUUIDUser]
	Name string `orm:"column:name"`
}

func (TestUUIDUser) TableName() string { return "test_uuid_users" }

// TestSoftDeleteUser is a SoftDeleteModel-backed model used in WithContext tests.
type TestSoftDeleteUser struct {
	SoftDeleteModel[TestSoftDeleteUser]
	Name string `orm:"column:name"`
}

func (TestSoftDeleteUser) TableName() string { return "test_soft_users" }

// TestSoftDeleteUUIDUser is a SoftDeleteUUIDModel-backed model used in WithContext tests.
type TestSoftDeleteUUIDUser struct {
	SoftDeleteUUIDModel[TestSoftDeleteUUIDUser]
	Name string `orm:"column:name"`
}

func (TestSoftDeleteUUIDUser) TableName() string { return "test_soft_uuid_users" }

// TestModel_WithContext_BindsCtx verifies WithContext on each of the four
// model variants returns a *Query[T] whose ctx is the one passed in. This
// is the regression test for the static-helpers-bypass-WithContext bug
// (item 3 in the upstream-bugs report).
func TestModel_WithContext_BindsCtx(t *testing.T) {
	type ctxKey string
	const k ctxKey = "tracer"
	ctx := context.WithValue(context.Background(), k, "abc-123")

	t.Run("Model[T]", func(t *testing.T) {
		q := Model[TestUser]{}.WithContext(ctx)
		if q == nil {
			t.Fatal("WithContext returned nil")
		}
		if q.ctx == nil {
			t.Fatal("Query.ctx is nil after WithContext")
		}
		if got := q.ctx.Value(k); got != "abc-123" {
			t.Errorf("ctx value not propagated: got %v, want abc-123", got)
		}
	})

	t.Run("UUIDModel[T]", func(t *testing.T) {
		q := UUIDModel[TestUUIDUser]{}.WithContext(ctx)
		if q == nil || q.ctx == nil {
			t.Fatal("WithContext returned nil or unbound ctx")
		}
		if got := q.ctx.Value(k); got != "abc-123" {
			t.Errorf("ctx value not propagated: got %v, want abc-123", got)
		}
	})

	t.Run("SoftDeleteModel[T]", func(t *testing.T) {
		q := SoftDeleteModel[TestSoftDeleteUser]{}.WithContext(ctx)
		if q == nil || q.ctx == nil {
			t.Fatal("WithContext returned nil or unbound ctx")
		}
		if got := q.ctx.Value(k); got != "abc-123" {
			t.Errorf("ctx value not propagated: got %v, want abc-123", got)
		}
	})

	t.Run("SoftDeleteUUIDModel[T]", func(t *testing.T) {
		q := SoftDeleteUUIDModel[TestSoftDeleteUUIDUser]{}.WithContext(ctx)
		if q == nil || q.ctx == nil {
			t.Fatal("WithContext returned nil or unbound ctx")
		}
		if got := q.ctx.Value(k); got != "abc-123" {
			t.Errorf("ctx value not propagated: got %v, want abc-123", got)
		}
	})
}

// TestModel_WithContext_PropagatesToDriver asserts that the bound ctx
// flows through to subsequent terminal calls (Get, Pluck, ...) by passing
// an already-cancelled context and confirming the driver returns the
// cancellation error.
func TestModel_WithContext_PropagatesToDriver(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "Alice", "alice@example.com", 30)

	// Cancelled-before-use ctx: any QueryContext call must surface
	// context.Canceled (or a wrapping error containing it).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Model[TestUser]{}.WithContext(ctx).Get()
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// TestModel_WithContext_SoftDeleteScopeStillApplies guards against a
// regression where the new WithContext entry point would skip the
// soft-delete predicate. SoftDeleteModel rows with deleted_at set must
// remain hidden from a default-scoped query.
func TestModel_WithContext_SoftDeleteScopeStillApplies(t *testing.T) {
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

	got, err := SoftDeleteModel[TestSoftDeleteUser]{}.WithContext(context.Background()).Get()
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("default-scoped Get returned %d rows, want 1 (soft-delete predicate dropped)", len(got))
	}
	if len(got) >= 1 && got[0].Name != "alive" {
		t.Errorf("got user %q, want 'alive'", got[0].Name)
	}
}

// TestModel_WithContext_DoesNotBreakStaticForm asserts the existing
// context-blind helpers still work after the WithContext addition. This
// is the backwards-compatibility check.
func TestModel_WithContext_DoesNotBreakStaticForm(t *testing.T) {
	setupConvenienceTests(t)
	id := seedUser(t, Default(), "Carol", "carol@example.com", 28)

	// Static form: Model[User]{}.Find(id) - still uses Background ctx.
	user, err := Model[TestUser]{}.Find(id)
	if err != nil {
		t.Fatalf("static Find returned error: %v", err)
	}
	if user.Name != "Carol" {
		t.Errorf("got name %q, want Carol", user.Name)
	}
}

// TestUUIDModel_WithContext_PropagatesToDriver mirrors the
// PropagatesToDriver test for the UUID variant. We seed via raw SQL so
// the test does not depend on Save (which would itself need a
// WithContext path; out of scope for this change).
func TestUUIDModel_WithContext_PropagatesToDriver(t *testing.T) {
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

	_, err := UUIDModel[TestUUIDUser]{}.WithContext(ctx).Get()
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}
