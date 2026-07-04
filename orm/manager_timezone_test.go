package orm

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/orm/drivers"
)

// TestNewManager_ForwardsTimeZone pins the DB_TIMEZONE plumbing: the
// session-timezone knob must survive ManagerConfig -> ConnectionConfig so
// the postgres/mysql DSN builders can emit it. SQLite ignores it, which
// makes it safe to assert on a real in-memory connection.
func TestNewManager_ForwardsTimeZone(t *testing.T) {
	m, err := NewManager(ManagerConfig{
		Driver:   "sqlite",
		Database: ":memory:",
		TimeZone: "Asia/Karachi",
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Shutdown(context.Background())

	d, ok := m.DefaultDriver().(*drivers.SQLiteDriver)
	if !ok {
		t.Fatalf("DefaultDriver is %T, want *drivers.SQLiteDriver", m.DefaultDriver())
	}
	if d.Config.TimeZone != "Asia/Karachi" {
		t.Errorf("ConnectionConfig.TimeZone = %q, want Asia/Karachi", d.Config.TimeZone)
	}
}
