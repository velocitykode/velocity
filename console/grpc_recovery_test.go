package console

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestMakeGRPCService_RerunSucceedsAfterBufConfigWriteFailure proves the
// partial-scaffold lockout is fixed: if a transient filesystem condition
// causes buf.yaml/buf.gen.yaml to fail to write, the user must be able to
// rerun cleanly once the condition is resolved. The previous order wrote
// foo.proto first; a config-write failure then left foo.proto on disk,
// and subsequent reruns failed with "proto already exists" before any
// impl or module was created.
func TestMakeGRPCService_RerunSucceedsAfterBufConfigWriteFailure(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires POSIX permissions and non-root user")
	}
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	// Make api/proto read-only so buf.yaml write fails on the first attempt.
	if err := os.MkdirAll(filepath.Join("api", "proto"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join("api", "proto"), 0555); err != nil {
		t.Fatal(err)
	}

	if err := MakeGRPCService("Foo", MakeGRPCServiceOptions{}); err == nil {
		t.Fatal("expected first MakeGRPCService to fail due to read-only api/proto")
	}

	// Verify no service-specific artifact landed on disk; if it had, the
	// rerun below would hit "proto already exists" / "service already
	// exists" and the bug would still be live.
	for _, p := range []string{
		filepath.Join("api", "proto", "foo", "v1", "foo.proto"),
		filepath.Join("internal", "grpc", "services", "foo.go"),
		filepath.Join("internal", "providers", "grpc_module.go"),
	} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("after failed first attempt, %s should not exist", p)
		}
	}

	// Resolve the filesystem condition and rerun.
	if err := os.Chmod(filepath.Join("api", "proto"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := MakeGRPCService("Foo", MakeGRPCServiceOptions{}); err != nil {
		t.Fatalf("rerun should succeed after fixing the filesystem, got: %v", err)
	}

	for _, p := range []string{
		filepath.Join("api", "proto", "buf.yaml"),
		filepath.Join("api", "proto", "buf.gen.yaml"),
		filepath.Join("api", "proto", "foo", "v1", "foo.proto"),
		filepath.Join("internal", "grpc", "services", "foo.go"),
		filepath.Join("internal", "providers", "grpc_module.go"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s after recovery rerun: %v", p, err)
		}
	}
}

// TestAppendRPCToProto_IgnoresCommentedServiceHeader is the regression test
// for the "header search not skipping comments" bug. A commented-out service
// declaration with the same name as the real one must not anchor the brace
// scan; otherwise the scan starts at a '{' inside a comment and either
// inserts into the wrong block or fails with "unbalanced braces".
func TestAppendRPCToProto_IgnoresCommentedServiceHeader(t *testing.T) {
	t.Chdir(t.TempDir())
	dir := filepath.Join("api", "proto", "foo", "v1")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "foo.proto")
	original := `syntax = "proto3";
package foo.v1;

// Old draft, kept for reference. Do not delete.
// service FooService {
//   rpc Legacy(LegacyRequest) returns (LegacyResponse);
// }

service FooService {
  rpc Get(GetRequest) returns (GetResponse);
}

message GetRequest {}
message GetResponse {}
`
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	if err := appendRPCToProto(path, "FooService", "List", grpcRPCUnary); err != nil {
		t.Fatalf("appendRPCToProto: %v", err)
	}
	got, _ := os.ReadFile(path)
	s := string(got)

	if balanceBraces(s) != 0 {
		t.Errorf("unbalanced braces after insert:\n%s", s)
	}

	// The commented draft must survive byte-for-byte.
	if !strings.Contains(s, "// Old draft, kept for reference. Do not delete.") ||
		!strings.Contains(s, "// service FooService {") ||
		!strings.Contains(s, "//   rpc Legacy(LegacyRequest) returns (LegacyResponse);") ||
		!strings.Contains(s, "// }") {
		t.Errorf("commented draft was damaged:\n%s", s)
	}

	// The new rpc must land inside the REAL service block, not before it.
	realServiceIdx := strings.Index(s, "\nservice FooService {")
	listIdx := strings.Index(s, "rpc List(")
	if realServiceIdx < 0 || listIdx < 0 {
		t.Fatalf("expected substrings not found:\n%s", s)
	}
	if listIdx < realServiceIdx {
		t.Errorf("new rpc was inserted before the real service block")
	}

	// And land before the existing Get rpc's terminator so the service
	// still parses as a single block.
	getEnd := strings.Index(s[realServiceIdx:], "\n}") + realServiceIdx
	if listIdx > getEnd {
		t.Errorf("new rpc was inserted after the real service's closing brace")
	}
}

// TestAppendRPCToProto_IgnoresServiceTokenInStringLiteral guards the string
// literal branch of the header search. A service-shaped fragment inside a
// quoted string (e.g. an option value documenting the API) must not be
// treated as a real service declaration.
func TestAppendRPCToProto_IgnoresServiceTokenInStringLiteral(t *testing.T) {
	t.Chdir(t.TempDir())
	dir := filepath.Join("api", "proto", "foo", "v1")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "foo.proto")
	original := `syntax = "proto3";
package foo.v1;

option (some.api) = "service FooService { fake }";

service FooService {
  rpc Get(GetRequest) returns (GetResponse);
}
`
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if err := appendRPCToProto(path, "FooService", "List", grpcRPCUnary); err != nil {
		t.Fatalf("appendRPCToProto: %v", err)
	}
	got, _ := os.ReadFile(path)
	s := string(got)
	if balanceBraces(s) != 0 {
		t.Errorf("unbalanced braces after insert:\n%s", s)
	}
	if !strings.Contains(s, `"service FooService { fake }"`) {
		t.Errorf("string literal was damaged:\n%s", s)
	}
	if !strings.Contains(s, "rpc List(ListRequest) returns (ListResponse);") {
		t.Errorf("List rpc missing:\n%s", s)
	}
}

// TestFindServiceBlock_RejectsServiceTokenInsideLongerIdentifier guards the
// word-boundary check. A service named `Foo` must not match a substring of
// `FooBar`, and `service` keyword must not match a substring of `serviceFoo`.
func TestFindServiceBlock_RejectsServiceTokenInsideLongerIdentifier(t *testing.T) {
	content := `syntax = "proto3";
package foo.v1;

// keyword embedded in a longer identifier
message serviceConfig {
  string name = 1;
}

service FooBar {
  rpc Ping(PingRequest) returns (PingResponse);
}

service Foo {
  rpc Get(GetRequest) returns (GetResponse);
}
`
	// Asking for "Foo" must NOT find the body of "FooBar".
	openIdx, _, err := findServiceBlock(content, "Foo")
	if err != nil {
		t.Fatalf("findServiceBlock Foo: %v", err)
	}
	// Verify openIdx is the '{' that follows `service Foo`, not the one
	// that follows `service FooBar`.
	prefix := content[:openIdx]
	if !strings.HasSuffix(strings.TrimRight(prefix, " \t"), "service Foo") {
		t.Errorf("openIdx anchored to wrong service header.\nprefix tail: %q", prefix[max0(len(prefix)-40):])
	}
}

func max0(i int) int {
	if i < 0 {
		return 0
	}
	return i
}
