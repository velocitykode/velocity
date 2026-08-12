package console

import (
	"os"
	"strings"
	"testing"
)

func TestGenMiddleware_CreatesFile(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := GenMiddleware("Auth", GenMiddlewareOptions{}); err != nil {
		t.Fatalf("GenMiddleware() error = %v", err)
	}

	content, err := os.ReadFile("internal/middleware/auth.go")
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	s := string(content)
	if !strings.Contains(s, "package middleware") {
		t.Error("expected package middleware")
	}
	if !strings.Contains(s, "func Auth(next router.HandlerFunc) router.HandlerFunc") {
		t.Error("expected middleware function signature")
	}
	if !strings.Contains(s, "return next(ctx)") {
		t.Error("expected next(ctx) call")
	}
}

func TestGenMiddleware_AlreadyExists(t *testing.T) {
	t.Chdir(t.TempDir())

	os.MkdirAll("internal/middleware", 0755)
	os.WriteFile("internal/middleware/auth.go", []byte("existing"), 0644)

	err := GenMiddleware("Auth", GenMiddlewareOptions{})
	if err == nil {
		t.Error("expected error when middleware already exists")
	}
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' in error, got: %v", err)
	}
}

func TestGenMiddleware_StripsSuffix(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := GenMiddleware("AuthMiddleware", GenMiddlewareOptions{}); err != nil {
		t.Fatalf("GenMiddleware() error = %v", err)
	}

	if _, err := os.Stat("internal/middleware/auth.go"); err != nil {
		t.Error("expected auth.go (Middleware suffix should be stripped)")
	}
}

func TestGenMiddleware_VerifiesContent(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := GenMiddleware("RateLimit", GenMiddlewareOptions{}); err != nil {
		t.Fatalf("GenMiddleware() error = %v", err)
	}

	content, err := os.ReadFile("internal/middleware/rate_limit.go")
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	s := string(content)
	if !strings.Contains(s, "// RateLimit middleware") {
		t.Error("expected comment with middleware name")
	}
	if !strings.Contains(s, "func RateLimit(") {
		t.Error("expected PascalCase function name")
	}
	if !strings.Contains(s, `"github.com/velocitykode/velocity/router"`) {
		t.Error("expected router import")
	}
}
