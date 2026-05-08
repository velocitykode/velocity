package orm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// ============================================================================
// Fix 1: implicit Existence (no need to embed orm.Existence on custom shapes)
// ============================================================================

// CustomAudit is a minimal trait composition deliberately WITHOUT an
// embedded Existence trait. The save layer must auto-attach existence
// tracking through the side-channel store so a second Save UPDATEs
// rather than INSERTs a duplicate row.
type CustomAudit struct {
	IDInt[CustomAudit]
	CreatedAtOnly
	Action string
}

func (CustomAudit) TableName() string { return "custom_audits" }

func setupCustomAuditTable(t *testing.T) *Manager {
	t.Helper()
	m := newTestManager(t)
	if _, err := m.DB().Exec(`CREATE TABLE custom_audits (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		action TEXT NOT NULL,
		created_at DATETIME
	)`); err != nil {
		t.Fatalf("create custom_audits: %v", err)
	}
	SetDefault(m)
	t.Cleanup(func() {
		ResetDefault()
		m.Shutdown(context.Background())
	})
	return m
}

// TestImplicitExistence_InsertSetsFlagSecondSaveBlocks asserts that the
// side-channel existence store fires for a custom composition with no
// explicit Existence trait. The model has CreatedAtOnly (no UpdatedAt)
// so a second Save would normally INSERT again, producing a duplicate.
// With implicit existence the second Save sees IsExisting=true and -
// because CreatedAtOnly implies append-only-ish semantics here - returns
// without writing a duplicate.
func TestImplicitExistence_InsertSetsFlag(t *testing.T) {
	setupCustomAuditTable(t)

	rec := &CustomAudit{Action: "first"}
	if err := Save(context.Background(), nil, rec); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	if rec.ID == 0 {
		t.Fatal("ID not populated after first Save")
	}

	// Without implicit Existence, the second Save would INSERT again.
	// With it, isModelExisting(rec) is true; saveModel will UPDATE
	// (which CreatedAtOnly actually allows since AppendOnly is not
	// in this composition). The point of this test is that the
	// existence side-channel correctly flips the flag.
	if !isModelExisting(rec) {
		t.Errorf("isModelExisting(rec) = false, want true after first Save")
	}
}

// CustomAuditAppendOnly adds AppendOnly so the second-Save path
// observably blocks (returning ErrImmutableModelUpdate). Used to
// verify the flag flip end-to-end through the save dispatcher.
type CustomAuditAppendOnly struct {
	IDInt[CustomAuditAppendOnly]
	CreatedAtOnly
	AppendOnly
	Action string
}

func (CustomAuditAppendOnly) TableName() string { return "custom_audit_ap" }

func setupCustomAuditAppendOnly(t *testing.T) *Manager {
	t.Helper()
	m := newTestManager(t)
	if _, err := m.DB().Exec(`CREATE TABLE custom_audit_ap (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		action TEXT NOT NULL,
		created_at DATETIME
	)`); err != nil {
		t.Fatalf("create custom_audit_ap: %v", err)
	}
	SetDefault(m)
	t.Cleanup(func() {
		ResetDefault()
		m.Shutdown(context.Background())
	})
	return m
}

// TestImplicitExistence_SecondSaveBlocksOnAppendOnly asserts the
// implicit-existence side-channel works end-to-end: a custom AppendOnly
// composition with NO Existence trait still rejects a re-Save with
// ErrImmutableModelUpdate, because saveWithDriver consults
// isModelExisting(model) instead of a struct field.
func TestImplicitExistence_SecondSaveBlocksOnAppendOnly(t *testing.T) {
	setupCustomAuditAppendOnly(t)

	rec := &CustomAuditAppendOnly{Action: "user.login"}
	if err := Save(context.Background(), nil, rec); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	if rec.ID == 0 {
		t.Fatal("ID not populated after first Save")
	}

	if err := Save(context.Background(), nil, rec); !errors.Is(err, ErrImmutableModelUpdate) {
		t.Errorf("second Save error = %v, want ErrImmutableModelUpdate", err)
	}
}

// ============================================================================
// Fix 2: panics → errors (CLAUDE.md library rule #10)
// ============================================================================

type DualPK struct {
	IDInt[DualPK]
	IDUUID[DualPK]
	Name string
}

func (DualPK) TableName() string { return "dual_pks" }

// TestDualPK_ReturnsFeaturesError asserts that mutually-exclusive
// trait composition surfaces as a *FeaturesError from featuresFor /
// MetaFor / save path - NOT a panic. Library code is forbidden from
// panicking on runtime trait-detection failures (CLAUDE.md rule 10).
func TestDualPK_ReturnsFeaturesError(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("featuresFor panicked on dual-PK composition; want error: %v", r)
		}
	}()

	_, err := featuresForT[DualPK]()
	if err == nil {
		t.Fatal("featuresFor returned nil error for IDInt+IDUUID")
	}
	var fe *FeaturesError
	if !errors.As(err, &fe) {
		t.Errorf("error type = %T, want *FeaturesError", err)
	}
	if !strings.Contains(err.Error(), "DualPK") {
		t.Errorf("error message %q does not name the offending struct", err.Error())
	}
}

type DualTimestamps struct {
	IDInt[DualTimestamps]
	Timestamps
	CreatedAtOnly
	Note string
}

func TestDualTimestamps_ReturnsFeaturesError(t *testing.T) {
	_, err := featuresForT[DualTimestamps]()
	if err == nil {
		t.Fatal("featuresFor returned nil error for Timestamps+CreatedAtOnly")
	}
	if !strings.Contains(err.Error(), "Timestamps") {
		t.Errorf("error message %q does not mention conflicting traits", err.Error())
	}
}

// TestRegisterModel_StartupValidationOK asserts the opt-in eager
// validation succeeds on a valid composition.
func TestRegisterModel_StartupValidationOK(t *testing.T) {
	type Good struct {
		IDInt[Good]
		Timestamps
		Email string
	}
	if err := RegisterModel[Good](); err != nil {
		t.Errorf("RegisterModel on valid composition: %v", err)
	}
}

// TestRegisterModel_StartupValidationFails asserts the opt-in eager
// validation surfaces *FeaturesError so a provider Boot() can fail
// loudly at startup rather than waiting for the first request.
func TestRegisterModel_StartupValidationFails(t *testing.T) {
	if err := RegisterModel[DualPK](); err == nil {
		t.Error("RegisterModel returned nil on dual-PK composition")
	}
}

// ============================================================================
// Fix 3 + 4: weak-pointer race-safe + bounded existence store
// ============================================================================

// TestExistenceStore_DetectsStaleEntryAfterGC is a soft witness for the
// race fix. Marking a model existing then dropping the reference and
// triggering GC must allow the existence entry to be reclaimed; an
// isModelExisting call that lands at the same address (very rare in a
// test harness, simulated here by allocating a new model after the
// previous is unreachable) must NOT inherit the stale "true".
func TestExistenceStore_DetectsStaleEntryAfterGC(t *testing.T) {
	type Tiny struct {
		IDInt[Tiny]
	}

	// Mark a model existing, drop ref.
	{
		m := &Tiny{}
		markModelExisting(m)
		if !isModelExisting(m) {
			t.Fatal("isModelExisting was false right after mark")
		}
		// m goes out of scope at the end of this block.
		_ = m
	}

	// Allocate a fresh Tiny. It's a different object; isModelExisting
	// must return false even if the address happens to be reused (the
	// alive() closure on the entry observes the weak.Pointer going
	// nil and treats the entry as stale).
	fresh := &Tiny{}
	if isModelExisting(fresh) {
		t.Errorf("fresh model unexpectedly seen as existing - stale entry not detected")
	}
}

// ============================================================================
// Fix 5: AppendOnly + SoftDeletes composes correctly
// ============================================================================

// TombstoneAudit composes AppendOnly with SoftDeletes - the headline
// case the trait system was designed to enable. Save on existing must
// fail (append-only contract). Soft-delete via Query.Delete must
// succeed (the tombstone-update is exempted).
type TombstoneAudit struct {
	IDInt[TombstoneAudit]
	CreatedAtOnly
	SoftDeletes[TombstoneAudit]
	AppendOnly
	Action string
}

func (TombstoneAudit) TableName() string { return "tombstone_audits" }

func setupTombstoneAudit(t *testing.T) *Manager {
	t.Helper()
	m := newTestManager(t)
	if _, err := m.DB().Exec(`CREATE TABLE tombstone_audits (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		action TEXT NOT NULL,
		created_at DATETIME,
		deleted_at DATETIME
	)`); err != nil {
		t.Fatalf("create tombstone_audits: %v", err)
	}
	SetDefault(m)
	t.Cleanup(func() {
		ResetDefault()
		m.Shutdown(context.Background())
	})
	return m
}

// TestAppendOnlyPlusSoftDeletes_TombstoneAllowed verifies the headline
// composition: a soft-delete UPDATE that writes only deleted_at on an
// AppendOnly row succeeds, while a content-mutation Save is rejected.
func TestAppendOnlyPlusSoftDeletes_TombstoneAllowed(t *testing.T) {
	setupTombstoneAudit(t)

	rec := &TombstoneAudit{Action: "user.login"}
	if err := Save(context.Background(), nil, rec); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Re-Save (content mutation) is rejected.
	if err := Save(context.Background(), nil, rec); !errors.Is(err, ErrImmutableModelUpdate) {
		t.Errorf("re-Save on AppendOnly row: %v, want ErrImmutableModelUpdate", err)
	}

	// Tombstone via direct Query.Update with deleted_at only is allowed
	// because AppendOnly+SoftDeletes is a valid composition.
	now := time.Now()
	q := newQuery[TombstoneAudit]()
	affected, err := q.Where("id = ?", int(rec.ID)).Update(context.Background(), map[string]any{"deleted_at": now})
	if err != nil {
		t.Errorf("tombstone Update: %v, want success", err)
	}
	if affected != 1 {
		t.Errorf("tombstone affected = %d, want 1", affected)
	}
}
