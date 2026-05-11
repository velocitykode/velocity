package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAppendRPCToProto_RpcWithOptionsBlockNotConfusedWithService is the
// regression test for the brace-counting bug: previously appendRPCToProto
// used a non-greedy regex that matched the first `\n}` after `service X {`.
// When a preceding rpc had a method options block (very common with
// grpc-gateway HTTP annotations), that `}` belonged to the rpc, not the
// service, and the new rpc was inserted inside the existing rpc block,
// breaking the proto.
//
// The test uses an annotated proto roughly shaped like grpc-gateway output
// and asserts that:
//   1. the new rpc appears at the correct nesting level (top-level inside
//      service, NOT inside another rpc),
//   2. the existing rpc's option block is preserved unchanged,
//   3. only one closing brace exists at column 0 (the service's), proving
//      the brace structure is still balanced.
func TestAppendRPCToProto_RpcWithOptionsBlockNotConfusedWithService(t *testing.T) {
	t.Chdir(t.TempDir())
	dir := filepath.Join("api", "proto", "foo", "v1")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "foo.proto")
	original := `syntax = "proto3";

package foo.v1;

import "google/api/annotations.proto";

option go_package = "acme/app/api/gen/go/foo/v1;foov1";

service FooService {
  rpc Get(GetRequest) returns (GetResponse) {
    option (google.api.http) = {
      get: "/v1/foo/{id}"
    };
  }
}

message GetRequest {
  string id = 1;
}

message GetResponse {
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

	if !strings.Contains(s, "rpc List(ListRequest) returns (ListResponse);") {
		t.Fatalf("new rpc missing from output:\n%s", s)
	}

	if !strings.Contains(s, `option (google.api.http) = {`) ||
		!strings.Contains(s, `get: "/v1/foo/{id}"`) {
		t.Errorf("existing rpc option block was damaged:\n%s", s)
	}

	listIdx := strings.Index(s, "rpc List(")
	getOptEnd := strings.Index(s, "}")
	getOptEnd = strings.Index(s[getOptEnd+1:], "}") + getOptEnd + 1 // close of rpc body
	if listIdx < getOptEnd {
		t.Errorf("new rpc was inserted before the existing rpc's closing brace:\n%s", s)
	}

	if balanceBraces(s) != 0 {
		t.Errorf("proto has unbalanced braces after insertion:\n%s", s)
	}

	if c := strings.Count(s, "\n}"); c < 2 {
		t.Errorf("expected at least two top-level closing braces (service + maybe message), got %d:\n%s", c, s)
	}
}

// TestAppendRPCToProto_BraceInStringLiteral guards the string-literal branch
// of findServiceBlock. A `}` character that appears inside a quoted string in
// a method option must not terminate the service block scan. While rare in
// proto3, it is legal in option values and any future contributor relying on
// the naive regex would corrupt such files.
func TestAppendRPCToProto_BraceInStringLiteral(t *testing.T) {
	t.Chdir(t.TempDir())
	dir := filepath.Join("api", "proto", "foo", "v1")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "foo.proto")
	original := `syntax = "proto3";
package foo.v1;
service FooService {
  rpc Quirky(QuirkyRequest) returns (QuirkyResponse) {
    option deprecated = true; // pattern with literal '}': "}"
  }
}
`
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if err := appendRPCToProto(path, "FooService", "Ping", grpcRPCUnary); err != nil {
		t.Fatalf("appendRPCToProto: %v", err)
	}
	got, _ := os.ReadFile(path)
	if balanceBraces(string(got)) != 0 {
		t.Errorf("unbalanced braces after insert:\n%s", got)
	}
	if !strings.Contains(string(got), "rpc Ping(") {
		t.Errorf("Ping rpc not added:\n%s", got)
	}
}

// TestAppendRPCToProto_BraceInBlockComment guards the /* ... */ branch.
// A reviewer adding a multi-line comment with `{` or `}` characters before
// the new rpc must not break the scan.
func TestAppendRPCToProto_BraceInBlockComment(t *testing.T) {
	t.Chdir(t.TempDir())
	dir := filepath.Join("api", "proto", "foo", "v1")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "foo.proto")
	original := `syntax = "proto3";
package foo.v1;
service FooService {
  /* curly: } inside a block comment {{{ should be ignored */
  rpc Get(GetRequest) returns (GetResponse);
}
`
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if err := appendRPCToProto(path, "FooService", "Stream", grpcRPCServerStream); err != nil {
		t.Fatalf("appendRPCToProto: %v", err)
	}
	got, _ := os.ReadFile(path)
	if balanceBraces(string(got)) != 0 {
		t.Errorf("unbalanced braces after insert:\n%s", got)
	}
	if !strings.Contains(string(got), "rpc Stream(StreamRequest) returns (stream StreamResponse);") {
		t.Errorf("Stream rpc not added or wrong shape:\n%s", got)
	}
}

// balanceBraces returns the net brace count, treating // and /* */ comments
// and "..." strings as opaque. A balanced proto returns 0.
func balanceBraces(s string) int {
	depth := 0
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == '/' && i+1 < len(s) && s[i+1] == '/':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			i += 2
			for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			i += 2
		case c == '"':
			i++
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' && i+1 < len(s) {
					i += 2
					continue
				}
				i++
			}
			i++
		case c == '{':
			depth++
			i++
		case c == '}':
			depth--
			i++
		default:
			i++
		}
	}
	return depth
}
