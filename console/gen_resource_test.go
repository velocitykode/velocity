package console

import (
	"os"
	"strings"
	"testing"
)

func TestGenResource_CreatesFile(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := GenResource("User", GenResourceOptions{}); err != nil {
		t.Fatalf("GenResource() error = %v", err)
	}

	content, err := os.ReadFile("internal/resources/user.go")
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	s := string(content)
	if !strings.Contains(s, "package resources") {
		t.Error("expected package resources")
	}
	if !strings.Contains(s, "UserResource") {
		t.Error("expected UserResource struct")
	}
	if !strings.Contains(s, "ToResource()") {
		t.Error("expected ToResource method")
	}
	if !strings.Contains(s, "map[string]any") {
		t.Error("expected map[string]any return type")
	}
}

func TestGenResource_StripsSuffix(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := GenResource("UserResource", GenResourceOptions{}); err != nil {
		t.Fatalf("GenResource() error = %v", err)
	}

	if _, err := os.Stat("internal/resources/user.go"); err != nil {
		t.Error("expected user.go (Resource suffix should be stripped)")
	}
}

func TestGenResource_AlreadyExists(t *testing.T) {
	t.Chdir(t.TempDir())

	os.MkdirAll("internal/resources", 0755)
	os.WriteFile("internal/resources/user.go", []byte("existing"), 0644)

	err := GenResource("User", GenResourceOptions{})
	if err == nil {
		t.Error("expected error when resource already exists")
	}
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' in error, got: %v", err)
	}
}

func TestGenResource_VerifiesContent(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := GenResource("BlogPost", GenResourceOptions{}); err != nil {
		t.Fatalf("GenResource() error = %v", err)
	}

	content, err := os.ReadFile("internal/resources/blog_post.go")
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	s := string(content)
	if !strings.Contains(s, "BlogPostResource") {
		t.Error("expected BlogPostResource struct")
	}
	if !strings.Contains(s, "func (r BlogPostResource) ToResource()") {
		t.Error("expected ToResource method on BlogPostResource")
	}
}
