package console

import (
	"os"
	"strings"
	"testing"
)

func TestMakeModule_CreatesFile(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := MakeModule("Cache", MakeModuleOptions{}); err != nil {
		t.Fatalf("MakeModule() error = %v", err)
	}

	content, err := os.ReadFile("internal/providers/cache.go")
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	s := string(content)
	if !strings.Contains(s, "package providers") {
		t.Error("expected package providers")
	}
	if !strings.Contains(s, "CacheModule") {
		t.Error("expected CacheModule struct")
	}
	if !strings.Contains(s, "Init(s *velocity.Services) error") {
		t.Error("expected Init method")
	}
	if !strings.Contains(s, "Start(s *velocity.Services) error") {
		t.Error("expected Start method")
	}
	if !strings.Contains(s, "Shutdown(ctx context.Context) error") {
		t.Error("expected Shutdown method")
	}
	if !strings.Contains(s, `"github.com/velocitykode/velocity"`) {
		t.Error("expected velocity import")
	}
}

func TestMakeModule_StripsSuffix(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := MakeModule("CacheProvider", MakeModuleOptions{}); err != nil {
		t.Fatalf("MakeModule() error = %v", err)
	}

	if _, err := os.Stat("internal/providers/cache.go"); err != nil {
		t.Error("expected cache.go (Provider suffix should be stripped)")
	}
}

func TestMakeModule_StripsServiceProviderSuffix(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := MakeModule("CacheServiceProvider", MakeModuleOptions{}); err != nil {
		t.Fatalf("MakeModule() error = %v", err)
	}

	if _, err := os.Stat("internal/providers/cache.go"); err != nil {
		t.Error("expected cache.go (ServiceProvider suffix should be stripped)")
	}
}

func TestMakeModule_StripsModuleSuffix(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := MakeModule("CacheModule", MakeModuleOptions{}); err != nil {
		t.Fatalf("MakeModule() error = %v", err)
	}

	if _, err := os.Stat("internal/providers/cache.go"); err != nil {
		t.Error("expected cache.go (Module suffix should be stripped)")
	}
}

func TestMakeModule_AlreadyExists(t *testing.T) {
	t.Chdir(t.TempDir())

	os.MkdirAll("internal/providers", 0755)
	os.WriteFile("internal/providers/cache.go", []byte("existing"), 0644)

	err := MakeModule("Cache", MakeModuleOptions{})
	if err == nil {
		t.Error("expected error when module already exists")
	}
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' in error, got: %v", err)
	}
}

func TestMakeModule_VerifiesContent(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := MakeModule("Payment", MakeModuleOptions{}); err != nil {
		t.Fatalf("MakeModule() error = %v", err)
	}

	content, err := os.ReadFile("internal/providers/payment.go")
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	s := string(content)
	if !strings.Contains(s, "PaymentModule") {
		t.Error("expected PaymentModule struct")
	}
	if !strings.Contains(s, `"context"`) {
		t.Error("expected context import")
	}
}
