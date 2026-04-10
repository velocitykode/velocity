package console

import (
	"os"
	"strings"
	"testing"
)

func TestMakePolicy_CreatesFile(t *testing.T) {
	chdir(t, t.TempDir())

	if err := MakePolicy("Post", MakePolicyOptions{}); err != nil {
		t.Fatalf("MakePolicy() error = %v", err)
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

func TestMakePolicy_StripsSuffix(t *testing.T) {
	chdir(t, t.TempDir())

	if err := MakePolicy("PostPolicy", MakePolicyOptions{}); err != nil {
		t.Fatalf("MakePolicy() error = %v", err)
	}

	if _, err := os.Stat("internal/policies/post.go"); err != nil {
		t.Error("expected post.go (Policy suffix should be stripped)")
	}
}

func TestMakePolicy_AlreadyExists(t *testing.T) {
	chdir(t, t.TempDir())

	os.MkdirAll("internal/policies", 0755)
	os.WriteFile("internal/policies/post.go", []byte("existing"), 0644)

	err := MakePolicy("Post", MakePolicyOptions{})
	if err == nil {
		t.Error("expected error when policy already exists")
	}
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' in error, got: %v", err)
	}
}

func TestMakePolicy_VerifiesContent(t *testing.T) {
	chdir(t, t.TempDir())

	if err := MakePolicy("Comment", MakePolicyOptions{}); err != nil {
		t.Fatalf("MakePolicy() error = %v", err)
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
