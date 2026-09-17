package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenSeeder_CreatesFile(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := GenSeeder("Role", GenSeederOptions{}); err != nil {
		t.Fatalf("GenSeeder() error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join("database", "seeders", "role.go"))
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	s := string(content)
	for _, want := range []string{
		"package seeders",
		`"context"`,
		`"github.com/velocitykode/velocity/orm"`,
		"type RoleSeeder struct{}",
		`return "role"`,
		"Run(ctx context.Context, db *orm.Manager) error",
		"Register this seeder in database/seeders/kernel.go",
		"r.Add(&RoleSeeder{})",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("generated seeder missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "seed.Register") || strings.Contains(s, "func init()") {
		t.Error("generated seeder must not self-register via init()")
	}
}

func TestGenSeeder_NameNormalisation(t *testing.T) {
	cases := []struct {
		input    string
		file     string
		typeName string
		name     string
	}{
		{"Role", "role.go", "RoleSeeder", `"role"`},
		{"RoleSeeder", "role.go", "RoleSeeder", `"role"`},
		{"user_profile", "user_profile.go", "UserProfileSeeder", `"user-profile"`},
		{"UserProfile", "user_profile.go", "UserProfileSeeder", `"user-profile"`},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if err := GenSeeder(tc.input, GenSeederOptions{}); err != nil {
				t.Fatalf("GenSeeder(%q) error = %v", tc.input, err)
			}
			content, err := os.ReadFile(filepath.Join("database", "seeders", tc.file))
			if err != nil {
				t.Fatalf("expected %s: %v", tc.file, err)
			}
			s := string(content)
			if !strings.Contains(s, "type "+tc.typeName+" struct{}") {
				t.Errorf("expected type %s in:\n%s", tc.typeName, s)
			}
			if !strings.Contains(s, "return "+tc.name) {
				t.Errorf("expected Name() to return %s in:\n%s", tc.name, s)
			}
		})
	}
}

func TestGenSeeder_SuffixOnlyNameRejected(t *testing.T) {
	t.Chdir(t.TempDir())
	err := GenSeeder("Seeder", GenSeederOptions{})
	if err == nil || !strings.Contains(err.Error(), "invalid seeder name") {
		t.Fatalf("GenSeeder(\"Seeder\") error = %v, want invalid seeder name", err)
	}
}

func TestGenSeeder_AlreadyExists(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := os.MkdirAll(filepath.Join("database", "seeders"), defaultDirMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("database", "seeders", "role.go"), []byte("existing"), defaultFileMode); err != nil {
		t.Fatal(err)
	}

	err := GenSeeder("Role", GenSeederOptions{})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("GenSeeder over existing file error = %v, want 'already exists'", err)
	}
}

func TestGenSeeder_DirOverride(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := GenSeeder("Role", GenSeederOptions{Dir: "db/seeders"}); err != nil {
		t.Fatalf("GenSeeder() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join("db", "seeders", "role.go")); err != nil {
		t.Errorf("expected db/seeders/role.go: %v", err)
	}
	if _, err := os.Stat(filepath.Join("database", "seeders")); !os.IsNotExist(err) {
		t.Error("default directory must not be created when --dir is given")
	}
}
