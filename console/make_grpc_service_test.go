package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFakeGoMod(t *testing.T, modulePath string) {
	t.Helper()
	content := "module " + modulePath + "\n\ngo 1.22\n"
	if err := os.WriteFile("go.mod", []byte(content), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
}

func TestMakeGRPCService_CreatesAll(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	if err := MakeGRPCService("Foo", MakeGRPCServiceOptions{}); err != nil {
		t.Fatalf("MakeGRPCService: %v", err)
	}

	cases := map[string][]string{
		filepath.Join("api", "proto", "foo", "v1", "foo.proto"): {
			"package foo.v1;",
			`option go_package = "acme/app/api/gen/go/foo/v1;foov1";`,
			"service FooService {",
		},
		filepath.Join("internal", "grpc", "services", "foo.go"): {
			"package services",
			`foopb "acme/app/api/gen/go/foo/v1"`,
			"type FooService struct {",
			"foopb.UnimplementedFooServiceServer",
			"func NewFooService() *FooService",
		},
		filepath.Join("internal", "providers", "grpc_provider.go"): {
			"// vel:grpc:imports",
			"// vel:grpc:services",
			"RegisterFooServiceServer(",
			"velgrpc.NewServer(",
		},
		filepath.Join("api", "proto", "buf.yaml"):     {"version: v2"},
		filepath.Join("api", "proto", "buf.gen.yaml"): {"buf.build/grpc/go"},
	}

	for path, needles := range cases {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("expected %s: %v", path, err)
			continue
		}
		for _, n := range needles {
			if !strings.Contains(string(data), n) {
				t.Errorf("%s missing %q", path, n)
			}
		}
	}
}

func TestMakeGRPCService_StripsServiceSuffix(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	if err := MakeGRPCService("BarService", MakeGRPCServiceOptions{}); err != nil {
		t.Fatalf("MakeGRPCService: %v", err)
	}
	if _, err := os.Stat(filepath.Join("api", "proto", "bar", "v1", "bar.proto")); err != nil {
		t.Errorf("expected bar.proto (Service suffix stripped): %v", err)
	}
}

func TestMakeGRPCService_TwoServicesWireBoth(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	if err := MakeGRPCService("Foo", MakeGRPCServiceOptions{}); err != nil {
		t.Fatalf("first service: %v", err)
	}
	if err := MakeGRPCService("Bar", MakeGRPCServiceOptions{}); err != nil {
		t.Fatalf("second service: %v", err)
	}

	data, err := os.ReadFile(filepath.Join("internal", "providers", "grpc_provider.go"))
	if err != nil {
		t.Fatalf("read provider: %v", err)
	}
	s := string(data)
	for _, n := range []string{
		"RegisterFooServiceServer(",
		"RegisterBarServiceServer(",
		`foopb "acme/app/api/gen/go/foo/v1"`,
		`barpb "acme/app/api/gen/go/bar/v1"`,
	} {
		if !strings.Contains(s, n) {
			t.Errorf("provider missing %q", n)
		}
	}
}

func TestMakeGRPCService_DuplicateFails(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	if err := MakeGRPCService("Foo", MakeGRPCServiceOptions{}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := MakeGRPCService("Foo", MakeGRPCServiceOptions{}); err == nil {
		t.Error("expected error on duplicate service")
	}
}

func TestMakeGRPCService_MissingGoMod(t *testing.T) {
	t.Chdir(t.TempDir())
	err := MakeGRPCService("Foo", MakeGRPCServiceOptions{})
	if err == nil {
		t.Error("expected error when go.mod missing")
	}
}

func TestMakeGRPCService_RejectsTraversal(t *testing.T) {
	cases := []string{
		"../../tmp/owned",
		"../tmp",
		"foo/../../../etc",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Chdir(tmp)
			writeFakeGoMod(t, "acme/app")

			err := MakeGRPCService(name, MakeGRPCServiceOptions{})
			if err == nil {
				t.Fatalf("expected error for %q", name)
			}
			if !strings.Contains(err.Error(), "invalid") && !strings.Contains(err.Error(), "parent traversal") {
				t.Errorf("expected traversal-rejection error, got %v", err)
			}
		})
	}
}

func TestMakeGRPCService_RejectsAbsolute(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	if err := MakeGRPCService("/tmp/Foo", MakeGRPCServiceOptions{}); err == nil {
		t.Fatal("expected error for absolute path service name")
	}
}

func TestMakeGRPCService_RejectsHiddenSegment(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	if err := MakeGRPCService(".hidden/Foo", MakeGRPCServiceOptions{}); err == nil {
		t.Fatal("expected error for hidden-prefixed segment")
	}
}
