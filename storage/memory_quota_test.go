package storage

import (
	"errors"
	"testing"
)

func TestMemoryDriver_MoveQuotaAccounting(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(*testing.T, *MemoryDriver)
		from        string
		to          string
		wantUsed    int64
		wantErr     error
		wantExists  []string
		wantMissing []string
	}{
		{
			name: "move to fresh key keeps used unchanged",
			setup: func(t *testing.T, driver *MemoryDriver) {
				t.Helper()
				mustPut(t, driver, "a", 100)
				mustPut(t, driver, "b", 200)
			},
			from:        "a",
			to:          "c",
			wantUsed:    300,
			wantExists:  []string{"b", "c"},
			wantMissing: []string{"a"},
		},
		{
			name: "move over existing destination subtracts overwritten bytes",
			setup: func(t *testing.T, driver *MemoryDriver) {
				t.Helper()
				mustPut(t, driver, "a", 100)
				mustPut(t, driver, "b", 200)
			},
			from:        "a",
			to:          "b",
			wantUsed:    100,
			wantExists:  []string{"b"},
			wantMissing: []string{"a"},
		},
		{
			name: "self move leaves used and file unchanged",
			setup: func(t *testing.T, driver *MemoryDriver) {
				t.Helper()
				mustPut(t, driver, "a", 100)
			},
			from:       "a",
			to:         "a",
			wantUsed:   100,
			wantExists: []string{"a"},
		},
		{
			name: "missing source leaves used unchanged",
			setup: func(t *testing.T, driver *MemoryDriver) {
				t.Helper()
				mustPut(t, driver, "b", 200)
			},
			from:       "missing",
			to:         "b",
			wantUsed:   200,
			wantErr:    ErrFileNotFound,
			wantExists: []string{"b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := NewMemoryDriver(DiskConfig{Driver: "memory"})
			tt.setup(t, driver)

			err := driver.Move(tt.from, tt.to)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Move() error = %v, want %v", err, tt.wantErr)
			}

			if driver.used != tt.wantUsed {
				t.Fatalf("used = %d, want %d", driver.used, tt.wantUsed)
			}

			for _, path := range tt.wantExists {
				if !driver.Exists(path) {
					t.Fatalf("expected %q to exist", path)
				}
			}
			for _, path := range tt.wantMissing {
				if driver.Exists(path) {
					t.Fatalf("expected %q to be missing", path)
				}
			}
		})
	}
}

func mustPut(t *testing.T, driver *MemoryDriver, path string, size int) {
	t.Helper()
	if err := driver.Put(path, make([]byte, size)); err != nil {
		t.Fatalf("Put(%q, %d bytes) error = %v", path, size, err)
	}
}
