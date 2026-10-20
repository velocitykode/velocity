package orm

import (
	"testing"
	"time"
)

// ManagerConfig.Validate rejects negative pool and timing values, but only
// when a Driver is set: a zero Driver means no database.
func TestManagerConfigValidate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     ManagerConfig
		wantErr bool
	}{
		{"zero config", ManagerConfig{}, false},
		{"valid", ManagerConfig{Driver: "sqlite", MaxIdleConns: 2, MaxOpenConns: 4, ConnMaxLifetime: time.Minute, SlowThreshold: time.Second}, false},
		{"negative MaxIdleConns", ManagerConfig{Driver: "sqlite", MaxIdleConns: -1}, true},
		{"negative MaxOpenConns", ManagerConfig{Driver: "sqlite", MaxOpenConns: -1}, true},
		{"negative ConnMaxLifetime", ManagerConfig{Driver: "sqlite", ConnMaxLifetime: -time.Second}, true},
		{"negative SlowThreshold", ManagerConfig{Driver: "sqlite", SlowThreshold: -time.Millisecond}, true},
		{"negatives without driver", ManagerConfig{MaxIdleConns: -1, MaxOpenConns: -1, ConnMaxLifetime: -1, SlowThreshold: -1}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
