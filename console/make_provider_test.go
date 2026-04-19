package console

import (
	"os"
	"strings"
	"testing"
)

func TestMakeProvider_CreatesFile(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := MakeProvider("Cache", MakeProviderOptions{}); err != nil {
		t.Fatalf("MakeProvider() error = %v", err)
	}

	content, err := os.ReadFile("internal/providers/cache.go")
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	s := string(content)
	if !strings.Contains(s, "package providers") {
		t.Error("expected package providers")
	}
	if !strings.Contains(s, "CacheServiceProvider") {
		t.Error("expected CacheServiceProvider struct")
	}
	if !strings.Contains(s, "Register(s *velocity.Services) error") {
		t.Error("expected Register method")
	}
	if !strings.Contains(s, "Boot(s *velocity.Services) error") {
		t.Error("expected Boot method")
	}
	if !strings.Contains(s, "Shutdown(ctx context.Context) error") {
		t.Error("expected Shutdown method")
	}
	if !strings.Contains(s, `"github.com/velocitykode/velocity"`) {
		t.Error("expected velocity import")
	}
}

func TestMakeProvider_StripsSuffix(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := MakeProvider("CacheProvider", MakeProviderOptions{}); err != nil {
		t.Fatalf("MakeProvider() error = %v", err)
	}

	if _, err := os.Stat("internal/providers/cache.go"); err != nil {
		t.Error("expected cache.go (Provider suffix should be stripped)")
	}
}

func TestMakeProvider_StripsServiceProviderSuffix(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := MakeProvider("CacheServiceProvider", MakeProviderOptions{}); err != nil {
		t.Fatalf("MakeProvider() error = %v", err)
	}

	if _, err := os.Stat("internal/providers/cache.go"); err != nil {
		t.Error("expected cache.go (ServiceProvider suffix should be stripped)")
	}
}

func TestMakeProvider_AlreadyExists(t *testing.T) {
	t.Chdir(t.TempDir())

	os.MkdirAll("internal/providers", 0755)
	os.WriteFile("internal/providers/cache.go", []byte("existing"), 0644)

	err := MakeProvider("Cache", MakeProviderOptions{})
	if err == nil {
		t.Error("expected error when provider already exists")
	}
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' in error, got: %v", err)
	}
}

func TestMakeProvider_VerifiesContent(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := MakeProvider("Payment", MakeProviderOptions{}); err != nil {
		t.Fatalf("MakeProvider() error = %v", err)
	}

	content, err := os.ReadFile("internal/providers/payment.go")
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	s := string(content)
	if !strings.Contains(s, "PaymentServiceProvider") {
		t.Error("expected PaymentServiceProvider struct")
	}
	if !strings.Contains(s, `"context"`) {
		t.Error("expected context import")
	}
}
