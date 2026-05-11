package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAppendRPCToProto_BlockCommentInsideHeader is the regression test for
// the header-skip bug: valid proto permits a /* ... */ block comment between
// the `service` keyword and its name. The previous space-only skip stopped
// at the '/' character and treated the declaration as not found.
func TestAppendRPCToProto_BlockCommentInsideHeader(t *testing.T) {
	t.Chdir(t.TempDir())
	dir := filepath.Join("api", "proto", "foo", "v1")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "foo.proto")
	original := `syntax = "proto3";
package foo.v1;

service /* primary public API */ FooService {
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
	if !strings.Contains(s, "rpc List(ListRequest) returns (ListResponse);") {
		t.Errorf("List rpc not added:\n%s", s)
	}
	if !strings.Contains(s, "/* primary public API */") {
		t.Errorf("header block comment was damaged:\n%s", s)
	}
}

// TestAppendRPCToProto_LineCommentBeforeOpenBrace covers the second valid
// shape: a // line comment between the service name and its opening '{'.
// The previous skip stopped at '/' and missed this declaration.
func TestAppendRPCToProto_LineCommentBeforeOpenBrace(t *testing.T) {
	t.Chdir(t.TempDir())
	dir := filepath.Join("api", "proto", "foo", "v1")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "foo.proto")
	original := `syntax = "proto3";
package foo.v1;

service FooService // exported via gateway
{
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
	if !strings.Contains(s, "rpc List(") {
		t.Errorf("List rpc not added:\n%s", s)
	}
	if !strings.Contains(s, "// exported via gateway") {
		t.Errorf("header line comment was damaged:\n%s", s)
	}

	// Sanity: the new rpc must be inserted before the service's closing
	// brace, not before the rpc Get line.
	listIdx := strings.Index(s, "rpc List(")
	getIdx := strings.Index(s, "rpc Get(")
	closeIdx := strings.LastIndex(s, "\n}")
	if !(getIdx < listIdx && listIdx < closeIdx) {
		t.Errorf("List rpc not placed inside the service block; got positions get=%d list=%d close=%d\n%s",
			getIdx, listIdx, closeIdx, s)
	}
}

// TestAppendRPCToProto_LineCommentBetweenKeywordAndName covers the gap
// between `service` and the name (the third valid header position).
func TestAppendRPCToProto_LineCommentBetweenKeywordAndName(t *testing.T) {
	t.Chdir(t.TempDir())
	dir := filepath.Join("api", "proto", "foo", "v1")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "foo.proto")
	original := `syntax = "proto3";
package foo.v1;

service // gateway-visible
FooService {
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
	if !strings.Contains(s, "rpc List(") {
		t.Errorf("List rpc not added:\n%s", s)
	}
	if !strings.Contains(s, "// gateway-visible") {
		t.Errorf("header comment was damaged:\n%s", s)
	}
}
