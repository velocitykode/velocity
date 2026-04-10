package console

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func chdir(t *testing.T, dir string) {
	t.Helper()
	original, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	t.Cleanup(func() { os.Chdir(original) })
}

func TestMakeModel_CreatesFile(t *testing.T) {
	chdir(t, t.TempDir())

	if err := MakeModel("User", MakeModelOptions{}); err != nil {
		t.Fatalf("MakeModel() error = %v", err)
	}

	content, err := os.ReadFile("internal/models/user.go")
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	s := string(content)
	if !strings.Contains(s, "package models") {
		t.Error("expected package models")
	}
	if !strings.Contains(s, "orm.Model[User]") {
		t.Error("expected orm.Model[User] embedding")
	}
	if !strings.Contains(s, `return "users"`) {
		t.Error("expected pluralized table name 'users'")
	}
	if !strings.Contains(s, "func (User) TableName()") {
		t.Error("expected TableName method")
	}
}

func TestMakeModel_UUID(t *testing.T) {
	chdir(t, t.TempDir())

	if err := MakeModel("Post", MakeModelOptions{UUID: true}); err != nil {
		t.Fatalf("MakeModel() error = %v", err)
	}

	content, err := os.ReadFile("internal/models/post.go")
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	if !strings.Contains(string(content), "orm.UUIDModel[Post]") {
		t.Error("expected orm.UUIDModel[Post] embedding")
	}
}

func TestMakeModel_SoftDeletes(t *testing.T) {
	chdir(t, t.TempDir())

	if err := MakeModel("Comment", MakeModelOptions{SoftDeletes: true}); err != nil {
		t.Fatalf("MakeModel() error = %v", err)
	}

	content, err := os.ReadFile("internal/models/comment.go")
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	if !strings.Contains(string(content), "orm.SoftDeleteModel[Comment]") {
		t.Error("expected orm.SoftDeleteModel[Comment] embedding")
	}
}

func TestMakeModel_UUIDAndSoftDeletes(t *testing.T) {
	chdir(t, t.TempDir())

	if err := MakeModel("Order", MakeModelOptions{UUID: true, SoftDeletes: true}); err != nil {
		t.Fatalf("MakeModel() error = %v", err)
	}

	content, err := os.ReadFile("internal/models/order.go")
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	if !strings.Contains(string(content), "orm.SoftDeleteUUIDModel[Order]") {
		t.Error("expected orm.SoftDeleteUUIDModel[Order] embedding")
	}
}

func TestMakeModel_AlreadyExists(t *testing.T) {
	chdir(t, t.TempDir())

	os.MkdirAll("internal/models", 0755)
	os.WriteFile("internal/models/user.go", []byte("existing"), 0644)

	err := MakeModel("User", MakeModelOptions{})
	if err == nil {
		t.Error("expected error when model already exists")
	}
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' in error, got: %v", err)
	}
}

func TestMakeModel_StripsSuffix(t *testing.T) {
	chdir(t, t.TempDir())

	if err := MakeModel("UserModel", MakeModelOptions{}); err != nil {
		t.Fatalf("MakeModel() error = %v", err)
	}

	if _, err := os.Stat("internal/models/user.go"); err != nil {
		t.Error("expected user.go (Model suffix should be stripped)")
	}
}

func TestMakeModel_WithMigration(t *testing.T) {
	chdir(t, t.TempDir())

	if err := MakeModel("Article", MakeModelOptions{Migration: true}); err != nil {
		t.Fatalf("MakeModel() error = %v", err)
	}

	if _, err := os.Stat("internal/models/article.go"); err != nil {
		t.Error("expected model file to be created")
	}

	entries, err := os.ReadDir("database/migrations")
	if err != nil {
		t.Fatalf("Failed to read migrations directory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 migration file, got %d", len(entries))
	}

	migrationFile := entries[0].Name()
	if !strings.Contains(migrationFile, "create_articles") {
		t.Errorf("expected migration name to contain 'create_articles', got %q", migrationFile)
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
	if !strings.Contains(s, `CreateTable("articles"`) {
		t.Error("expected CreateTable with 'articles' table name")
	}
}

func TestMakeModel_WithMigrationUUIDSoftDeletes(t *testing.T) {
	chdir(t, t.TempDir())

	if err := MakeModel("Product", MakeModelOptions{Migration: true, UUID: true, SoftDeletes: true}); err != nil {
		t.Fatalf("MakeModel() error = %v", err)
	}

	entries, err := os.ReadDir("database/migrations")
	if err != nil {
		t.Fatalf("Failed to read migrations directory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 migration file, got %d", len(entries))
	}

	content, err := os.ReadFile(filepath.Join("database/migrations", entries[0].Name()))
	if err != nil {
		t.Fatalf("Failed to read migration file: %v", err)
	}
	s := string(content)

	if !strings.Contains(s, "UUIDPrimary") {
		t.Error("expected migration to use UUIDPrimary")
	}
	if !strings.Contains(s, "SoftDeletes") {
		t.Error("expected migration to include SoftDeletes")
	}
}

func TestToTableName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"User", "users"},
		{"Post", "posts"},
		{"Category", "categories"},
		{"UserProfile", "user_profiles"},
	}

	for _, tt := range tests {
		got := toTableName(tt.input)
		if got != tt.expected {
			t.Errorf("toTableName(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestMakeMigration_TimestampFormat(t *testing.T) {
	chdir(t, t.TempDir())

	if err := MakeMigration("create_tags", MakeMigrationOptions{}); err != nil {
		t.Fatalf("MakeMigration() error = %v", err)
	}

	entries, err := os.ReadDir("database/migrations")
	if err != nil {
		t.Fatalf("Failed to read migrations directory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 migration file, got %d", len(entries))
	}

	name := entries[0].Name()
	pattern := regexp.MustCompile(`^\d{14}_create_tags\.go$`)
	if !pattern.MatchString(name) {
		t.Errorf("filename %q does not match expected pattern YYYYMMDDHHMMSS_create_tags.go", name)
	}

	// Verify the version in the file content matches the filename prefix
	content, err := os.ReadFile(filepath.Join("database/migrations", name))
	if err != nil {
		t.Fatalf("Failed to read migration file: %v", err)
	}
	version := name[:14]
	if !strings.Contains(string(content), `"`+version+`"`) {
		t.Error("expected Version field in file to match filename timestamp")
	}
}
