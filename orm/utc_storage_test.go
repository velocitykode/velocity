package orm

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// tzStorageRecord exercises every ORM-managed timestamp column (created_at,
// updated_at via Timestamps; deleted_at via SoftDeletes).
type tzStorageRecord struct {
	SoftDeleteModel[tzStorageRecord]
	Name string `orm:"column:name"`
}

func (tzStorageRecord) TableName() string { return "tz_storage_records" }

func (tzStorageRecord) Fillable() []string { return []string{"name"} }

// TestUTCStorage_NonUTCHost is the regression test for the storage
// contract: a writer whose process timezone is not UTC must store the same
// wall clock a UTC writer would (the velship incident: a TZ=Asia/Karachi
// plane and a UTC plane sharing one database produced negative durations).
//
// time.Local is process-global and cannot be mutated safely under -race,
// so the parent re-execs the test binary with TZ=Asia/Karachi in the child
// environment (same re-exec pattern as log/file's lock test) and the child
// runs the real assertions against an in-memory SQLite database.
func TestUTCStorage_NonUTCHost(t *testing.T) {
	if os.Getenv("VELOCITY_TZ_STORAGE_CHILD") == "1" {
		runUTCStorageAssertions(t)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run", "^TestUTCStorage_NonUTCHost$", "-test.v")
	cmd.Env = append(os.Environ(),
		"VELOCITY_TZ_STORAGE_CHILD=1",
		"TZ=Asia/Karachi",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("non-UTC child failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "PASS") {
		t.Fatalf("non-UTC child produced no PASS:\n%s", out)
	}
}

func runUTCStorageAssertions(t *testing.T) {
	// Guard: the child must actually be running in a non-UTC zone, or the
	// assertions below prove nothing.
	if _, offset := time.Now().Zone(); offset != 5*3600 {
		t.Fatalf("child not running in Asia/Karachi (offset %d); TZ env not applied", offset)
	}

	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	if _, err := manager.DB().Exec(`
		CREATE TABLE tz_storage_records (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME
		)
	`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	SetDefault(manager)
	t.Cleanup(ResetDefault)
	ctx := context.Background()

	rec := &tzStorageRecord{Name: "incident"}
	if err := Save(ctx, manager, rec); err != nil {
		t.Fatalf("save: %v", err)
	}

	// The struct fields the app sees after Save are already UTC.
	if rec.CreatedAt.Location() != time.UTC {
		t.Errorf("Save left CreatedAt in %v, want UTC", rec.CreatedAt.Location())
	}

	// Auto-stamped insert: stored wall clock is the UTC wall clock, not
	// the (+5h) Karachi one.
	assertStoredUTC(t, manager, "created_at", rec.ID)
	assertStoredUTC(t, manager, "updated_at", rec.ID)

	// Map-based bulk Update stamps updated_at app-side in UTC, and a
	// caller-supplied LOCAL time value is rebased at bind time.
	karachiNoon := time.Date(2026, 7, 4, 14, 15, 0, 0, time.Local)
	if _, err := (Model[tzStorageRecord]{}).Where("id = ?", rec.ID).Update(ctx, map[string]any{
		"created_at": karachiNoon,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	assertStoredUTC(t, manager, "updated_at", rec.ID)
	storedCreated := storedText(t, manager, "created_at", rec.ID)
	if !strings.Contains(storedCreated, "09:15:00") || !strings.Contains(storedCreated, "+0000 UTC") {
		t.Errorf("caller-supplied 14:15+05:00 stored as %q, want 09:15 UTC wall clock", storedCreated)
	}

	// Round-trip: the scanned instant matches and surfaces in time.UTC.
	got, err := (SoftDeleteModel[tzStorageRecord]{}).Find(ctx, rec.ID)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got.CreatedAt.Location() != time.UTC {
		t.Errorf("scanned CreatedAt location = %v, want UTC", got.CreatedAt.Location())
	}
	if !got.CreatedAt.Equal(karachiNoon) {
		t.Errorf("round-trip changed instant: got %v, want %v", got.CreatedAt, karachiNoon)
	}

	// Soft delete stamps deleted_at app-side in UTC.
	if _, err := (Model[tzStorageRecord]{}).Where("id = ?", rec.ID).Delete(ctx); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	assertStoredUTC(t, manager, "deleted_at", rec.ID)

	// Incident shape: with created_at/deleted_at both stored UTC the
	// duration is non-negative even though the writer runs at +05:00.
	created := parseStored(t, storedCreated)
	deleted := parseStored(t, storedText(t, manager, "deleted_at", rec.ID))
	if deleted.Before(created) {
		t.Errorf("negative duration: deleted %v before created %v", deleted, created)
	}
}

// storedText returns the raw TEXT stored in the column, bypassing the
// driver's time parsing and the ORM's read-side UTC rebase.
func storedText(t *testing.T, m *Manager, column string, id uint) string {
	t.Helper()
	rows, err := m.Raw(context.Background(),
		"SELECT CAST("+column+" AS TEXT) FROM tz_storage_records WHERE id = ?", id)
	if err != nil {
		t.Fatalf("raw select %s: %v", column, err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("no row for id %d", id)
	}
	var s string
	if err := rows.Scan(&s); err != nil {
		t.Fatalf("scan %s: %v", column, err)
	}
	return s
}

// parseStored parses the modernc default write format (Go's t.String()).
func parseStored(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse("2006-01-02 15:04:05.999999999 -0700 MST", s)
	if err != nil {
		t.Fatalf("stored text %q does not parse as Go time.String(): %v", s, err)
	}
	return parsed
}

// assertStoredUTC asserts the column's stored wall clock equals the current
// UTC wall clock (tolerance 5m) and carries a zero offset - i.e. what a UTC
// host would have written.
func assertStoredUTC(t *testing.T, m *Manager, column string, id uint) {
	t.Helper()
	s := storedText(t, m, column, id)
	if !strings.Contains(s, "+0000 UTC") {
		t.Errorf("%s stored with non-UTC offset: %q", column, s)
		return
	}
	parsed := parseStored(t, s)
	if d := time.Since(parsed); d < -5*time.Minute || d > 5*time.Minute {
		t.Errorf("%s wall clock %v not within 5m of UTC now %v", column, parsed, time.Now().UTC())
	}
}
