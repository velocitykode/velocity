package orm_test

import (
	"context"
	"testing"
	"time"

	"github.com/velocitykode/velocity/orm"
	ormtesting "github.com/velocitykode/velocity/orm/testing"
)

// touchTracker embeds the integer-PK trait plus a timestamp field that is
// tagged autoUpdateTime but is NOT named UpdatedAt. The legacy Save path
// resolved its managed timestamp fields by Go field name (FieldByName
// "CreatedAt"/"UpdatedAt"), so a differently-named tagged field like this was
// never stamped. The cached-IndexPath optimization must preserve that: it
// keys on the field name, not on the autoCreateTime/autoUpdateTime role flag,
// so TouchedAt stays untouched by Save.
type touchTracker struct {
	orm.IDInt[touchTracker]
	Name      string    `orm:"column:name"`
	TouchedAt time.Time `orm:"autoUpdateTime" json:"touched_at"`
}

func (touchTracker) TableName() string { return "touch_trackers" }

func setupAliasTest(t *testing.T) *orm.Manager {
	t.Helper()
	manager, err := orm.NewManager(orm.ManagerConfig{Driver: "sqlite", Database: ":memory:"})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	db := manager.DB()
	if _, err := db.Exec(`CREATE TABLE touch_trackers (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		touched_at DATETIME
	)`); err != nil {
		t.Fatalf("create touch_trackers: %v", err)
	}
	orm.SetDefault(manager)
	t.Cleanup(func() {
		orm.ResetDefault()
		manager.Shutdown(context.Background())
	})
	return manager
}

// TestSave_AutoUpdateTimeAliasFieldNotStamped proves Save does not stamp a
// field tagged autoUpdateTime that is not named UpdatedAt, on both the insert
// and update paths. A regression here (keying on the role flag instead of the
// field name) would mutate TouchedAt to time.Now().
func TestSave_AutoUpdateTimeAliasFieldNotStamped(t *testing.T) {
	m := setupAliasTest(t)
	ctx := context.Background()

	// Insert via Save: TouchedAt must remain zero.
	rec := &touchTracker{Name: "a"}
	if err := orm.Save(ctx, nil, rec); err != nil {
		t.Fatalf("Save (insert): %v", err)
	}
	if rec.ID == 0 {
		t.Fatal("expected non-zero id after insert")
	}
	if !rec.TouchedAt.IsZero() {
		t.Errorf("TouchedAt stamped on insert: %v, want zero (field is not named UpdatedAt)", rec.TouchedAt)
	}

	// Update via Save: TouchedAt must still remain zero.
	rec.Name = "b"
	if err := orm.Save(ctx, nil, rec); err != nil {
		t.Fatalf("Save (update): %v", err)
	}
	if !rec.TouchedAt.IsZero() {
		t.Errorf("TouchedAt stamped on update: %v, want zero", rec.TouchedAt)
	}

	ormtesting.AssertDatabaseHas(t, m, "touch_trackers", map[string]any{"id": rec.ID, "name": "b"})
}
