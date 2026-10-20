package orm

import (
	"testing"
	"time"
)

// Config.Validate rejects negative pool and timing values, but only
// when a Driver is set: a zero Driver means no database.
func TestManagerConfigValidate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"zero config", Config{}, false},
		{"valid", Config{Driver: "sqlite", MaxIdleConns: 2, MaxOpenConns: 4, ConnMaxLifetime: time.Minute, SlowThreshold: time.Second}, false},
		{"negative MaxIdleConns", Config{Driver: "sqlite", MaxIdleConns: -1}, true},
		{"negative MaxOpenConns", Config{Driver: "sqlite", MaxOpenConns: -1}, true},
		{"negative ConnMaxLifetime", Config{Driver: "sqlite", ConnMaxLifetime: -time.Second}, true},
		{"negative SlowThreshold", Config{Driver: "sqlite", SlowThreshold: -time.Millisecond}, true},
		{"negatives without driver", Config{MaxIdleConns: -1, MaxOpenConns: -1, ConnMaxLifetime: -1, SlowThreshold: -1}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
