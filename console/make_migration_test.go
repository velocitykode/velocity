package console

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestMakeMigration_CreatesFile(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := MakeMigration("create_posts", MakeMigrationOptions{}); err != nil {
		t.Fatalf("MakeMigration() error = %v", err)
	}

	entries, err := os.ReadDir("database/migrations")
	if err != nil {
		t.Fatalf("Failed to read migrations directory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 migration file, got %d", len(entries))
	}

	migrationFile := entries[0].Name()
	pattern := regexp.MustCompile(`^\d{14}_create_posts\.go$`)
	if !pattern.MatchString(migrationFile) {
		t.Errorf("expected filename matching YYYYMMDDHHMMSS_create_posts.go, got %q", migrationFile)
	}

	content, err := os.ReadFile(filepath.Join("database/migrations", migrationFile))
	if err != nil {
		t.Fatalf("Failed to read migration file: %v", err)
	}

	s := string(content)
	if !strings.Contains(s, "package migrations") {
		t.Error("expected package migrations")
	}
	if !strings.Contains(s, "migrate.Register") {
		t.Error("expected migrate.Register call")
	}
}

func TestMakeMigration_CreateTable(t *testing.T) {
	t.Chdir(t.TempDir())

	err := MakeMigration("create_users", MakeMigrationOptions{Create: "users"})
	if err != nil {
		t.Fatalf("MakeMigration() error = %v", err)
	}

	entries, err := os.ReadDir("database/migrations")
	if err != nil {
		t.Fatalf("Failed to read migrations directory: %v", err)
	}
	content, err := os.ReadFile(filepath.Join("database/migrations", entries[0].Name()))
	if err != nil {
		t.Fatalf("Failed to read migration file: %v", err)
	}
	s := string(content)

	if !strings.Contains(s, `CreateTable("users"`) {
		t.Error("expected CreateTable with 'users' table name")
	}
	if !strings.Contains(s, `DropTable("users"`) {
		t.Error("expected DropTable in Down function")
	}
	if !strings.Contains(s, "t.ID()") {
		t.Error("expected t.ID() for auto-increment primary key")
	}
}

func TestMakeMigration_CreateTableUUID(t *testing.T) {
	t.Chdir(t.TempDir())

	err := MakeMigration("create_orders", MakeMigrationOptions{Create: "orders", UUID: true})
	if err != nil {
		t.Fatalf("MakeMigration() error = %v", err)
	}

	entries, err := os.ReadDir("database/migrations")
	if err != nil {
		t.Fatalf("Failed to read migrations directory: %v", err)
	}
	content, err := os.ReadFile(filepath.Join("database/migrations", entries[0].Name()))
	if err != nil {
		t.Fatalf("Failed to read migration file: %v", err)
	}
	s := string(content)

	if !strings.Contains(s, "UUIDPrimary") {
		t.Error("expected UUIDPrimary for UUID model")
	}
	if strings.Contains(s, "t.ID()") {
		t.Error("should NOT contain t.ID() when using UUID")
	}
}

func TestMakeMigration_CreateTableSoftDeletes(t *testing.T) {
	t.Chdir(t.TempDir())

	err := MakeMigration("create_posts", MakeMigrationOptions{Create: "posts", SoftDeletes: true})
	if err != nil {
		t.Fatalf("MakeMigration() error = %v", err)
	}

	entries, err := os.ReadDir("database/migrations")
	if err != nil {
		t.Fatalf("Failed to read migrations directory: %v", err)
	}
	content, err := os.ReadFile(filepath.Join("database/migrations", entries[0].Name()))
	if err != nil {
		t.Fatalf("Failed to read migration file: %v", err)
	}

	if !strings.Contains(string(content), "SoftDeletes") {
		t.Error("expected SoftDeletes column")
	}
}

func TestMakeMigration_AlterTable(t *testing.T) {
	t.Chdir(t.TempDir())

	err := MakeMigration("add_slug_to_posts", MakeMigrationOptions{Table: "posts"})
	if err != nil {
		t.Fatalf("MakeMigration() error = %v", err)
	}

	entries, err := os.ReadDir("database/migrations")
	if err != nil {
		t.Fatalf("Failed to read migrations directory: %v", err)
	}
	content, err := os.ReadFile(filepath.Join("database/migrations", entries[0].Name()))
	if err != nil {
		t.Fatalf("Failed to read migration file: %v", err)
	}
	s := string(content)

	if strings.Contains(s, "CreateTable") {
		t.Error("should NOT use CreateTable for alter-table migration")
	}
	if !strings.Contains(s, `m.Table("posts"`) {
		t.Error("expected m.Table(\"posts\" in alter-table migration")
	}
}

func TestMakeMigration_AlreadyExists(t *testing.T) {
	t.Chdir(t.TempDir())

	// Create first migration
	if err := MakeMigration("create_tags", MakeMigrationOptions{}); err != nil {
		t.Fatalf("first MakeMigration() error = %v", err)
	}

	// Get the filename to recreate it
	entries, _ := os.ReadDir("database/migrations")
	if len(entries) == 0 {
		t.Fatal("expected migration file")
	}

	// Try writing to the same path — since timestamps are per-second,
	// calling again in the same second would produce the same filename
	existingPath := filepath.Join("database/migrations", entries[0].Name())
	err := MakeMigration("create_tags", MakeMigrationOptions{})
	// If same second, should get overwrite guard; if different second, different file
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		t.Errorf("unexpected error: %v", err)
	}
	_ = existingPath
}

func TestMakeMigration_DistinctVersionsSameSecond(t *testing.T) {
	t.Chdir(t.TempDir())

	// Pin the clock so both calls deterministically compute the SAME second.
	// Without the collision guard both would derive an identical 14-digit
	// version, which makes migrate.Register panic ("duplicate migration
	// version") at app boot. The guard must bump the second so the versions
	// are distinct. Pinning (rather than relying on wall-clock back-to-back
	// timing) forces the collision every run, including the boundary case
	// where a real second would roll over between the two calls.
	fixed := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	orig := timeNow
	timeNow = func() time.Time { return fixed }
	t.Cleanup(func() { timeNow = orig })

	if err := MakeMigration("create_first", MakeMigrationOptions{}); err != nil {
		t.Fatalf("first MakeMigration() error = %v", err)
	}
	if err := MakeMigration("create_second", MakeMigrationOptions{}); err != nil {
		t.Fatalf("second MakeMigration() error = %v", err)
	}

	entries, err := os.ReadDir("database/migrations")
	if err != nil {
		t.Fatalf("Failed to read migrations directory: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 migration files, got %d", len(entries))
	}

	seen := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if len(name) < 14 {
			t.Fatalf("migration filename %q too short for a 14-digit version", name)
		}
		version := name[:14]
		if seen[version] {
			t.Errorf("duplicate migration version prefix %q across migrations (collision)", version)
		}
		seen[version] = true
	}
}

func TestToDescription(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"create_users", "Create users"},
		{"add_slug_to_posts", "Add slug to posts"},
		{"initialize", "Initialize"},
		{"", ""},
	}

	for _, tt := range tests {
		got := toDescription(tt.input)
		if got != tt.expected {
			t.Errorf("toDescription(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}
