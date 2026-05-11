package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadModulePath_TrimsModuleDirective(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("go.mod", []byte("module    acme/app   \n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := readModulePath()
	if err != nil {
		t.Fatalf("readModulePath: %v", err)
	}
	if got != "acme/app" {
		t.Errorf("expected trimmed module path, got %q", got)
	}
}

func TestReadModulePath_MissingDirective(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("go.mod", []byte("// no module directive here\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readModulePath(); err == nil {
		t.Error("expected error when go.mod lacks module directive")
	}
}

func TestReadModulePath_MissingFile(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, err := readModulePath(); err == nil {
		t.Error("expected error when go.mod absent")
	}
}

// TestAppendRPCToProto_MissingServiceBlock guards the failure mode where the
// user (or a future code path) hands us a proto file that does not declare
// the expected service. Silently doing nothing would lead to a confusing
// runtime error later when buf generate produces a service without the rpc.
func TestAppendRPCToProto_MissingServiceBlock(t *testing.T) {
	t.Chdir(t.TempDir())
	dir := filepath.Join("api", "proto", "foo", "v1")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "foo.proto")
	if err := os.WriteFile(path, []byte(`syntax = "proto3";

package foo.v1;
`), 0644); err != nil {
		t.Fatal(err)
	}

	err := appendRPCToProto(path, "FooService", "Hello", grpcRPCUnary)
	if err == nil {
		t.Fatal("expected error when service block absent")
	}
	if !strings.Contains(err.Error(), "FooService not found") {
		t.Errorf("error should name the missing service, got: %v", err)
	}
}

// TestProtoRPCLineAndMethodSignaturePair ensures the proto declaration and
// the Go method signature agree on streaming direction for every kind.
// A mismatch (e.g. proto says "stream resp" but Go expects unary) produces
// code that does not compile against the generated grpc interface; pinning
// the pair in one place prevents drift.
func TestProtoRPCLineAndMethodSignaturePair(t *testing.T) {
	cases := []struct {
		name        string
		kind        grpcRPCKind
		protoSubstr string
		goSubstr    string
	}{
		{"unary", grpcRPCUnary,
			"rpc Ping(PingRequest) returns (PingResponse);",
			"func (s *FooService) Ping(ctx context.Context, req *foov1.PingRequest) (*foov1.PingResponse, error)"},
		{"server stream", grpcRPCServerStream,
			"rpc Ping(PingRequest) returns (stream PingResponse);",
			"func (s *FooService) Ping(req *foov1.PingRequest, stream foov1.FooService_PingServer) error"},
		{"client stream", grpcRPCClientStream,
			"rpc Ping(stream PingRequest) returns (PingResponse);",
			"func (s *FooService) Ping(stream foov1.FooService_PingServer) error"},
		{"bidi", grpcRPCBidi,
			"rpc Ping(stream PingRequest) returns (stream PingResponse);",
			"func (s *FooService) Ping(stream foov1.FooService_PingServer) error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := protoRPCLine("Ping", tc.kind); got != tc.protoSubstr {
				t.Errorf("proto line: want %q, got %q", tc.protoSubstr, got)
			}
			if got := goMethodSignature("FooService", "Ping", "foov1", tc.kind); got != tc.goSubstr {
				t.Errorf("go signature: want %q, got %q", tc.goSubstr, got)
			}
		})
	}
}

// TestGoMethodBody_NilReturnsMatchSignatureArity verifies the body's return
// shape matches the signature: unary needs two returns (nil, nil), streaming
// needs one (nil). A mismatched body would not compile.
func TestGoMethodBody_NilReturnsMatchSignatureArity(t *testing.T) {
	if body := goMethodBody(grpcRPCUnary); !strings.Contains(body, "return nil, nil") {
		t.Errorf("unary body should return (nil, nil), got %q", body)
	}
	for _, k := range []grpcRPCKind{grpcRPCServerStream, grpcRPCClientStream, grpcRPCBidi} {
		body := goMethodBody(k)
		if !strings.Contains(body, "return nil") || strings.Contains(body, "return nil, nil") {
			t.Errorf("streaming body should return a single nil, got %q (kind=%d)", body, k)
		}
	}
}
