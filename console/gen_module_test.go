package console

import (
	"os"
	"strings"
	"testing"
)

func TestGenModule_CreatesFile(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := GenModule("Cache", GenModuleOptions{}); err != nil {
		t.Fatalf("GenModule() error = %v", err)
	}

	content, err := os.ReadFile("internal/modules/cache.go")
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	s := string(content)
	if !strings.Contains(s, "package modules") {
		t.Error("expected package modules")
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

// TestGenModule_TakesNameLiterally pins that only a Module suffix is
// normalised away. Any other trailing word is part of the name the user
// asked for and reaches the file name and the struct name untouched.
func TestGenModule_TakesNameLiterally(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := GenModule("CacheProvider", GenModuleOptions{}); err != nil {
		t.Fatalf("GenModule() error = %v", err)
	}

	content, err := os.ReadFile("internal/modules/cache_provider.go")
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}
	if !strings.Contains(string(content), "CacheProviderModule") {
		t.Error("expected CacheProviderModule struct")
	}
}

func TestGenModule_StripsModuleSuffix(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := GenModule("CacheModule", GenModuleOptions{}); err != nil {
		t.Fatalf("GenModule() error = %v", err)
	}

	if _, err := os.Stat("internal/modules/cache.go"); err != nil {
		t.Error("expected cache.go (Module suffix should be stripped)")
	}
}

func TestGenModule_AlreadyExists(t *testing.T) {
	t.Chdir(t.TempDir())

	os.MkdirAll("internal/modules", 0755)
	os.WriteFile("internal/modules/cache.go", []byte("existing"), 0644)

	err := GenModule("Cache", GenModuleOptions{})
	if err == nil {
		t.Error("expected error when module already exists")
	}
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' in error, got: %v", err)
	}
}

func TestGenModule_VerifiesContent(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := GenModule("Payment", GenModuleOptions{}); err != nil {
		t.Fatalf("GenModule() error = %v", err)
	}

	content, err := os.ReadFile("internal/modules/payment.go")
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

// TestGenModule_SuffixOnlyName pins the validate-then-normalise ordering: the
// raw argument passes scaffold.ValidateName, but stripping the redundant
// "Module" suffix can leave nothing behind. Such a name must be rejected
// rather than written out as "internal/modules/.go", which Go ignores.
func TestGenModule_SuffixOnlyName(t *testing.T) {
	tests := []struct {
		name     string
		arg      string
		wantErr  bool
		wantFile string
	}{
		{name: "pascal suffix alone", arg: "Module", wantErr: true},
		{name: "lower suffix alone", arg: "module", wantErr: true},
		{name: "normal name", arg: "Cache", wantFile: "internal/modules/cache.go"},
		{name: "normal name with suffix", arg: "CacheModule", wantFile: "internal/modules/cache.go"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())

			err := GenModule(tt.arg, GenModuleOptions{})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("GenModule(%q) = nil, want error", tt.arg)
				}
				if _, statErr := os.Stat("internal/modules/.go"); statErr == nil {
					t.Error("GenModule wrote internal/modules/.go, which the Go toolchain ignores")
				}
				return
			}
			if err != nil {
				t.Fatalf("GenModule(%q) error = %v", tt.arg, err)
			}
			if _, statErr := os.Stat(tt.wantFile); statErr != nil {
				t.Errorf("expected %s: %v", tt.wantFile, statErr)
			}
		})
	}
}
