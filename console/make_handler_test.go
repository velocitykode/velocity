package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMakeHandler_CreatesFile(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := MakeHandler("User", MakeHandlerOptions{}); err != nil {
		t.Fatalf("MakeHandler() error = %v", err)
	}

	content, err := os.ReadFile("internal/handlers/user.go")
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	if !strings.Contains(string(content), "package handlers") {
		t.Error("expected package handlers")
	}
	if !strings.Contains(string(content), "User") {
		t.Error("expected handler name User in content")
	}
}

func TestMakeHandler_NestedPath(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := MakeHandler("Admin/Dashboard", MakeHandlerOptions{}); err != nil {
		t.Fatalf("MakeHandler() error = %v", err)
	}

	path := filepath.Join("internal/handlers/admin", "dashboard.go")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected file at %s", path)
	}
}

func TestMakeHandler_AlreadyExists(t *testing.T) {
	t.Chdir(t.TempDir())

	os.MkdirAll("internal/handlers", 0755)
	os.WriteFile("internal/handlers/user.go", []byte("existing"), 0644)

	err := MakeHandler("User", MakeHandlerOptions{})
	if err == nil {
		t.Error("expected error when handler already exists")
	}
}

func TestMakeHandler_StripsSuffix(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := MakeHandler("UserHandler", MakeHandlerOptions{}); err != nil {
		t.Fatalf("MakeHandler() error = %v", err)
	}

	// Should strip "Handler" suffix — file should be user.go not user_handler.go
	if _, err := os.Stat("internal/handlers/user.go"); err != nil {
		t.Error("expected user.go (Handler suffix should be stripped)")
	}
}

func TestMakeHandler_RejectsTraversal(t *testing.T) {
	cases := []string{
		"../../tmp/owned",
		"../tmp",
		"foo/../../../etc",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Chdir(tmp)

			err := MakeHandler(name, MakeHandlerOptions{})
			if err == nil {
				t.Fatalf("expected error for %q", name)
			}
			if !strings.Contains(err.Error(), "invalid") && !strings.Contains(err.Error(), "parent traversal") {
				t.Errorf("expected traversal-rejection error, got %v", err)
			}
			// Confirm nothing was written outside the handler root.
			if _, statErr := os.Stat(filepath.Join(tmp, "..", "owned")); statErr == nil {
				t.Errorf("file written outside project root for %q", name)
			}
		})
	}
}

func TestMakeHandler_RejectsAbsolutePath(t *testing.T) {
	t.Chdir(t.TempDir())

	abs := "/tmp/velocity-test-abs"
	if err := MakeHandler(abs, MakeHandlerOptions{}); err == nil {
		t.Fatalf("expected error for absolute path %q", abs)
	}
}

func TestMakeHandler_RejectsHiddenSegment(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := MakeHandler(".hidden/foo", MakeHandlerOptions{}); err == nil {
		t.Fatal("expected error for hidden-prefixed segment")
	}
}

func TestMakeHandler_AllowsValidNamespace(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := MakeHandler("admin/users", MakeHandlerOptions{}); err != nil {
		t.Fatalf("expected admin/users to be accepted, got %v", err)
	}
	if _, err := os.Stat(filepath.Join("internal/handlers/admin", "users.go")); err != nil {
		t.Errorf("expected admin/users.go to be created: %v", err)
	}
}

func TestToSnakeCase(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"User", "user"},
		{"UserProfile", "user_profile"},
		{"Dashboard", "dashboard"},
	}

	for _, tt := range tests {
		got := toSnakeCase(tt.input)
		if got != tt.expected {
			t.Errorf("toSnakeCase(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestToPascalCase(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"user", "User"},
		{"user_profile", "UserProfile"},
		{"dashboard", "Dashboard"},
		{"hello-world", "HelloWorld"},
	}

	for _, tt := range tests {
		got := toPascalCase(tt.input)
		if got != tt.expected {
			t.Errorf("toPascalCase(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}
