package orm

import (
	"context"
	"testing"
	"time"
)

type scanTZRecord struct {
	Model[scanTZRecord]
	Name string `orm:"column:name"`
}

func (scanTZRecord) TableName() string { return "scan_tz_records" }

func (scanTZRecord) AssignableFields() []string { return []string{"name"} }

// TestScannedTimestampsSurfaceUTC pins the read side of the storage
// contract: whatever location the driver hands back (modernc sqlite
// preserves the stored offset, so a row written by an old non-UTC binary
// comes back in a FixedZone), the ORM surfaces time.Time values located in
// time.UTC with the instant preserved - across struct scans, Value, and
// Pluck.
func TestScannedTimestampsSurfaceUTC(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	if _, err := manager.DB().Exec(`
		CREATE TABLE scan_tz_records (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			created_at DATETIME,
			updated_at DATETIME
		)
	`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	SetDefault(manager)
	t.Cleanup(ResetDefault)
	ctx := context.Background()

	// Simulate a legacy row written by a +05:00 host before the UTC
	// contract: raw text with a non-zero offset, bypassing the bind-side
	// normalizer.
	const legacy = "2026-07-04 14:15:00 +0500 PKT"
	if _, err := manager.DB().Exec(
		`INSERT INTO scan_tz_records (name, created_at, updated_at) VALUES (?, ?, ?)`,
		"legacy", legacy, legacy,
	); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	wantInstant := time.Date(2026, 7, 4, 9, 15, 0, 0, time.UTC)

	t.Run("struct scan", func(t *testing.T) {
		got, err := (Model[scanTZRecord]{}).First(ctx)
		if err != nil {
			t.Fatalf("first: %v", err)
		}
		if got.CreatedAt.Location() != time.UTC {
			t.Errorf("CreatedAt location = %v, want UTC", got.CreatedAt.Location())
		}
		if !got.CreatedAt.Equal(wantInstant) {
			t.Errorf("CreatedAt = %v, want instant %v", got.CreatedAt, wantInstant)
		}
	})

	t.Run("Value", func(t *testing.T) {
		v, err := (Model[scanTZRecord]{}).Where("name = ?", "legacy").Value(ctx, "created_at")
		if err != nil {
			t.Fatalf("value: %v", err)
		}
		tm, ok := v.(time.Time)
		if !ok {
			t.Fatalf("Value returned %T, want time.Time", v)
		}
		if tm.Location() != time.UTC || !tm.Equal(wantInstant) {
			t.Errorf("Value = %v (%v), want %v in UTC", tm, tm.Location(), wantInstant)
		}
	})

	t.Run("insert map with RawSQL sentinel emits DB-clock expression", func(t *testing.T) {
		q := &Query[scanTZRecord]{table: "scan_tz_records", driver: manager.DefaultDriver()}
		id, err := q.InsertGetId(ctx, map[string]any{
			"name":       "sentinel",
			"created_at": CurrentTimestamp,
		})
		if err != nil {
			t.Fatalf("insert with sentinel: %v", err)
		}

		// The stored value must be a real DB-clock timestamp, not the
		// bound literal string "CURRENT_TIMESTAMP".
		v, err := (Model[scanTZRecord]{}).Where("id = ?", id).Value(ctx, "created_at")
		if err != nil {
			t.Fatalf("value: %v", err)
		}
		tm, ok := v.(time.Time)
		if !ok {
			t.Fatalf("stored created_at = %T (%v), want time.Time (sentinel was bound, not emitted)", v, v)
		}
		if tm.Location() != time.UTC {
			t.Errorf("sentinel timestamp location = %v, want UTC", tm.Location())
		}
		if d := time.Since(tm); d < -5*time.Minute || d > 5*time.Minute {
			t.Errorf("sentinel timestamp %v not within 5m of UTC now", tm)
		}
	})

	t.Run("Pluck", func(t *testing.T) {
		vs, err := (Model[scanTZRecord]{}).Where("name = ?", "legacy").Pluck(ctx, "created_at")
		if err != nil {
			t.Fatalf("pluck: %v", err)
		}
		if len(vs) != 1 {
			t.Fatalf("pluck returned %d values, want 1", len(vs))
		}
		tm, ok := vs[0].(time.Time)
		if !ok {
			t.Fatalf("Pluck returned %T, want time.Time", vs[0])
		}
		if tm.Location() != time.UTC || !tm.Equal(wantInstant) {
			t.Errorf("Pluck = %v (%v), want %v in UTC", tm, tm.Location(), wantInstant)
		}
	})
}
