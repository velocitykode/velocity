package console

import (
	"os"
	"strings"
	"testing"
)

func TestGenPolicy_CreatesFile(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := GenPolicy("Post", GenPolicyOptions{}); err != nil {
		t.Fatalf("GenPolicy() error = %v", err)
	}

	content, err := os.ReadFile("internal/policies/post.go")
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	s := string(content)
	if !strings.Contains(s, "package policies") {
		t.Error("expected package policies")
	}
	if !strings.Contains(s, "PostPolicy") {
		t.Error("expected PostPolicy struct")
	}
	if !strings.Contains(s, "func (p PostPolicy) View(") {
		t.Error("expected View method")
	}
	if !strings.Contains(s, "func (p PostPolicy) Create(") {
		t.Error("expected Create method")
	}
	if !strings.Contains(s, "func (p PostPolicy) Update(") {
		t.Error("expected Update method")
	}
	if !strings.Contains(s, "func (p PostPolicy) Delete(") {
		t.Error("expected Delete method")
	}
	if !strings.Contains(s, "*http.Request") {
		t.Error("expected *http.Request parameter")
	}
}

func TestGenPolicy_StripsSuffix(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := GenPolicy("PostPolicy", GenPolicyOptions{}); err != nil {
		t.Fatalf("GenPolicy() error = %v", err)
	}

	if _, err := os.Stat("internal/policies/post.go"); err != nil {
		t.Error("expected post.go (Policy suffix should be stripped)")
	}
}

func TestGenPolicy_AlreadyExists(t *testing.T) {
	t.Chdir(t.TempDir())

	os.MkdirAll("internal/policies", 0755)
	os.WriteFile("internal/policies/post.go", []byte("existing"), 0644)

	err := GenPolicy("Post", GenPolicyOptions{})
	if err == nil {
		t.Error("expected error when policy already exists")
	}
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' in error, got: %v", err)
	}
}

func TestGenPolicy_VerifiesContent(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := GenPolicy("Comment", GenPolicyOptions{}); err != nil {
		t.Fatalf("GenPolicy() error = %v", err)
	}

	content, err := os.ReadFile("internal/policies/comment.go")
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	s := string(content)
	if !strings.Contains(s, "CommentPolicy") {
		t.Error("expected CommentPolicy struct")
	}
	if !strings.Contains(s, `"net/http"`) {
		t.Error("expected net/http import")
	}
}
