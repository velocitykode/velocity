package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureContextImport_BlockFormAddsContext(t *testing.T) {
	src := `package services

import (
	foov1 "acme/app/api/gen/go/foo/v1"
)

type FooService struct{}
`
	got := ensureContextImport(src)
	if !strings.Contains(got, `"context"`) {
		t.Errorf("expected context to be added, got:\n%s", got)
	}
	if !strings.Contains(got, `foov1 "acme/app/api/gen/go/foo/v1"`) {
		t.Errorf("existing imports must be preserved, got:\n%s", got)
	}
	if c := strings.Count(got, "import ("); c != 1 {
		t.Errorf("expected exactly one import block, got %d", c)
	}
}

func TestEnsureContextImport_SingleLineFormPromotedToBlock(t *testing.T) {
	src := `package services

import "acme/app/api/gen/go/foo/v1"

type FooService struct{}
`
	got := ensureContextImport(src)
	if !strings.Contains(got, "import (") {
		t.Errorf("single-line import should be promoted to block form, got:\n%s", got)
	}
	if !strings.Contains(got, `"context"`) {
		t.Errorf("expected context to be added, got:\n%s", got)
	}
	if !strings.Contains(got, `"acme/app/api/gen/go/foo/v1"`) {
		t.Errorf("original import must survive promotion, got:\n%s", got)
	}
	if strings.Contains(got, `import "acme/app/api/gen/go/foo/v1"`) {
		t.Errorf("original single-line form should be replaced, got:\n%s", got)
	}
}

// TestMakeGRPCRPC_AddsContextImportOnFirstUnary protects the contract between
// the scaffold stub (which omits "context") and appendMethodToImpl: a freshly
// created service has no context import, so the first unary rpc must inject
// it. Server-stream-only services should NOT acquire the import since their
// signatures do not take a ctx parameter.
func TestMakeGRPCRPC_AddsContextImportOnFirstUnary(t *testing.T) {
	t.Chdir(t.TempDir())
	setupServiceForRPC(t)

	impl, _ := os.ReadFile(filepath.Join("internal", "grpc", "services", "foo.go"))
	if strings.Contains(string(impl), `"context"`) {
		t.Fatalf("precondition: scaffold should not import context yet, got:\n%s", impl)
	}

	if err := MakeGRPCRPC("Foo", "Hello", MakeGRPCRPCOptions{}); err != nil {
		t.Fatalf("MakeGRPCRPC unary: %v", err)
	}

	impl, _ = os.ReadFile(filepath.Join("internal", "grpc", "services", "foo.go"))
	if !strings.Contains(string(impl), `"context"`) {
		t.Errorf("first unary rpc must inject context import, got:\n%s", impl)
	}
	if c := strings.Count(string(impl), `"context"`); c != 1 {
		t.Errorf("expected exactly one context import, got %d:\n%s", c, impl)
	}
}

func TestMakeGRPCRPC_DoesNotDuplicateContextOnSecondUnary(t *testing.T) {
	t.Chdir(t.TempDir())
	setupServiceForRPC(t)

	if err := MakeGRPCRPC("Foo", "Hello", MakeGRPCRPCOptions{}); err != nil {
		t.Fatalf("first rpc: %v", err)
	}
	if err := MakeGRPCRPC("Foo", "Bye", MakeGRPCRPCOptions{}); err != nil {
		t.Fatalf("second rpc: %v", err)
	}

	impl, _ := os.ReadFile(filepath.Join("internal", "grpc", "services", "foo.go"))
	if c := strings.Count(string(impl), `"context"`); c != 1 {
		t.Errorf("expected exactly one context import after two unary rpcs, got %d:\n%s", c, impl)
	}
}

// TestMakeGRPCRPC_StreamOnlyDoesNotAddContext verifies the asymmetry: a
// server-stream rpc signature has no ctx parameter, so we must avoid
// injecting an unused "context" import that would fail `go build`.
func TestMakeGRPCRPC_StreamOnlyDoesNotAddContext(t *testing.T) {
	t.Chdir(t.TempDir())
	setupServiceForRPC(t)

	if err := MakeGRPCRPC("Foo", "Tail", MakeGRPCRPCOptions{Stream: true}); err != nil {
		t.Fatalf("MakeGRPCRPC stream: %v", err)
	}
	impl, _ := os.ReadFile(filepath.Join("internal", "grpc", "services", "foo.go"))
	if strings.Contains(string(impl), `"context"`) {
		t.Errorf("stream-only rpc should not inject context import, got:\n%s", impl)
	}
}
