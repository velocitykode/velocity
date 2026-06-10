package drivers

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestFileStore_MaxValueBytes proves the opt-in per-value cap on the file
// driver: oversized Put/Add/Forever fail with ErrValueTooLarge and store
// nothing; values within the cap, and any value on an uncapped store, are
// accepted unchanged.
func TestFileStore_MaxValueBytes(t *testing.T) {
	big := strings.Repeat("x", 1024)

	capped, err := NewFileStore("cap", t.TempDir(), WithFileMaxValueBytes(64))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if got := capped.MaxValueBytes(); got != 64 {
		t.Fatalf("MaxValueBytes() = %d, want 64", got)
	}

	if err := capped.Put("big", big, time.Hour); !errors.Is(err, ErrValueTooLarge) {
		t.Errorf("Put oversize: err = %v, want ErrValueTooLarge", err)
	}
	if _, ok := capped.Get("big"); ok {
		t.Error("oversized Put stored a value despite the error")
	}
	if ok, err := capped.Add("big-add", big, time.Hour); ok || !errors.Is(err, ErrValueTooLarge) {
		t.Errorf("Add oversize = (%v, %v), want (false, ErrValueTooLarge)", ok, err)
	}
	if err := capped.Forever("big-forever", big); !errors.Is(err, ErrValueTooLarge) {
		t.Errorf("Forever oversize: err = %v, want ErrValueTooLarge", err)
	}

	if err := capped.Put("small", "ok", time.Hour); err != nil {
		t.Errorf("Put within cap: %v", err)
	}
	if v, ok := capped.Get("small"); !ok || v != "ok" {
		t.Errorf("Get small = (%v, %v), want (ok, true)", v, ok)
	}

	// Default remains unlimited.
	unlimited, err := NewFileStore("nocap", t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if got := unlimited.MaxValueBytes(); got != 0 {
		t.Fatalf("default MaxValueBytes() = %d, want 0", got)
	}
	if err := unlimited.Put("big", big, time.Hour); err != nil {
		t.Errorf("Put on uncapped store: %v", err)
	}
}

// TestMemoryStore_MaxValueBytes mirrors the file-driver cap test for the
// memory driver, including PutMany's all-or-nothing size validation.
func TestMemoryStore_MaxValueBytes(t *testing.T) {
	big := strings.Repeat("x", 1024)

	capped := NewMemoryStore("cap", WithMaxValueBytes(64))
	if got := capped.MaxValueBytes(); got != 64 {
		t.Fatalf("MaxValueBytes() = %d, want 64", got)
	}

	if err := capped.Put("big", big, time.Hour); !errors.Is(err, ErrValueTooLarge) {
		t.Errorf("Put oversize: err = %v, want ErrValueTooLarge", err)
	}
	if _, ok := capped.Get("big"); ok {
		t.Error("oversized Put stored a value despite the error")
	}
	if ok, err := capped.Add("big-add", big, time.Hour); ok || !errors.Is(err, ErrValueTooLarge) {
		t.Errorf("Add oversize = (%v, %v), want (false, ErrValueTooLarge)", ok, err)
	}
	if err := capped.Forever("big-forever", big); !errors.Is(err, ErrValueTooLarge) {
		t.Errorf("Forever oversize: err = %v, want ErrValueTooLarge", err)
	}

	if err := capped.Put("small", "ok", time.Hour); err != nil {
		t.Errorf("Put within cap: %v", err)
	}

	// PutMany validates the whole batch before storing anything.
	err := capped.PutMany(map[string]interface{}{"fits": "ok", "huge": big}, time.Hour)
	if !errors.Is(err, ErrValueTooLarge) {
		t.Errorf("PutMany with oversize item: err = %v, want ErrValueTooLarge", err)
	}
	if _, ok := capped.Get("fits"); ok {
		t.Error("PutMany stored part of a batch that failed size validation")
	}

	// Default remains unlimited.
	unlimited := NewMemoryStore("nocap")
	if got := unlimited.MaxValueBytes(); got != 0 {
		t.Fatalf("default MaxValueBytes() = %d, want 0", got)
	}
	if err := unlimited.Put("big", big, time.Hour); err != nil {
		t.Errorf("Put on uncapped store: %v", err)
	}
}
