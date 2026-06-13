package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratorGenerate(t *testing.T) {
	tmp := t.TempDir()
	t.Chdir(tmp)

	result, err := Generator{
		DefaultDir: "internal/tools",
		Kind:       "tool",
		Stub:       "package {{ .Package }}\n\ntype {{ .Name }} struct{}\n",
	}.Generate("ListUsers", "custom/tools", map[string]any{
		"Package": "tools",
		"Name":    "ListUsers",
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	wantPath := filepath.Join("custom", "tools", "list_users.go")
	if result.Path != wantPath {
		t.Fatalf("Result.Path = %q, want %q", result.Path, wantPath)
	}
	if len(result.Paths) != 1 || result.Paths[0] != wantPath {
		t.Fatalf("Result.Paths = %#v, want [%q]", result.Paths, wantPath)
	}

	content, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read generated file: %v", err)
	}
	if got := string(content); !strings.Contains(got, "type ListUsers struct{}") {
		t.Fatalf("generated content missing rendered data:\n%s", got)
	}
}

func TestGeneratorGenerateRejectsUnsafeTarget(t *testing.T) {
	tmp := t.TempDir()
	outside := t.TempDir()
	t.Chdir(tmp)

	if err := os.Symlink(outside, filepath.Join(tmp, "custom")); err != nil {
		t.Fatalf("setup symlink: %v", err)
	}

	_, err := Generator{
		DefaultDir: "internal/tools",
		Kind:       "tool",
		Stub:       "package tools\n",
	}.Generate("ListUsers", "custom/tools", nil)
	if err == nil {
		t.Fatal("Generate accepted --dir routed through a symlink")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "tools", "list_users.go")); statErr == nil {
		t.Fatalf("generated file escaped through symlink")
	}
}

func TestValidateName(t *testing.T) {
	if err := ValidateName("SendEmail"); err != nil {
		t.Fatalf("ValidateName accepted name returned error: %v", err)
	}
	if err := ValidateName("Admin/Users"); err == nil {
		t.Fatal("ValidateName accepted nested name")
	}
	if err := ValidateNestedName("Admin/Users"); err != nil {
		t.Fatalf("ValidateNestedName rejected nested name: %v", err)
	}
}
