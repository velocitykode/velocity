package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupServiceForRPC(t *testing.T) {
	t.Helper()
	writeFakeGoMod(t, "acme/app")
	if err := GenGRPCService("Foo", GenGRPCServiceOptions{}); err != nil {
		t.Fatalf("seed service: %v", err)
	}
}

func TestGenGRPCRPC_UnaryAddsRpcAndMethod(t *testing.T) {
	t.Chdir(t.TempDir())
	setupServiceForRPC(t)

	if err := GenGRPCRPC("Foo", "Hello", GenGRPCRPCOptions{}); err != nil {
		t.Fatalf("GenGRPCRPC: %v", err)
	}

	proto, _ := os.ReadFile(filepath.Join("api", "proto", "foo", "v1", "foo.proto"))
	for _, n := range []string{
		"rpc Hello(HelloRequest) returns (HelloResponse);",
		"message HelloRequest {",
		"message HelloResponse {",
	} {
		if !strings.Contains(string(proto), n) {
			t.Errorf("proto missing %q", n)
		}
	}

	impl, _ := os.ReadFile(filepath.Join("internal", "grpc", "services", "foo.go"))
	if !strings.Contains(string(impl), "func (s *FooService) Hello(ctx context.Context, req *foopb.HelloRequest) (*foopb.HelloResponse, error)") {
		t.Errorf("impl missing unary Hello method, got:\n%s", impl)
	}
}

func TestGenGRPCRPC_ServerStream(t *testing.T) {
	t.Chdir(t.TempDir())
	setupServiceForRPC(t)

	if err := GenGRPCRPC("Foo", "Tail", GenGRPCRPCOptions{Stream: true}); err != nil {
		t.Fatalf("GenGRPCRPC: %v", err)
	}
	proto, _ := os.ReadFile(filepath.Join("api", "proto", "foo", "v1", "foo.proto"))
	if !strings.Contains(string(proto), "rpc Tail(TailRequest) returns (stream TailResponse);") {
		t.Errorf("proto missing server-stream rpc, got:\n%s", proto)
	}
	impl, _ := os.ReadFile(filepath.Join("internal", "grpc", "services", "foo.go"))
	if !strings.Contains(string(impl), "func (s *FooService) Tail(req *foopb.TailRequest, stream foopb.FooService_TailServer) error") {
		t.Errorf("impl missing server-stream method, got:\n%s", impl)
	}
}

func TestGenGRPCRPC_ClientStream(t *testing.T) {
	t.Chdir(t.TempDir())
	setupServiceForRPC(t)

	if err := GenGRPCRPC("Foo", "Upload", GenGRPCRPCOptions{ClientStream: true}); err != nil {
		t.Fatalf("GenGRPCRPC: %v", err)
	}
	proto, _ := os.ReadFile(filepath.Join("api", "proto", "foo", "v1", "foo.proto"))
	if !strings.Contains(string(proto), "rpc Upload(stream UploadRequest) returns (UploadResponse);") {
		t.Errorf("proto missing client-stream rpc, got:\n%s", proto)
	}
	impl, _ := os.ReadFile(filepath.Join("internal", "grpc", "services", "foo.go"))
	if !strings.Contains(string(impl), "func (s *FooService) Upload(stream foopb.FooService_UploadServer) error") {
		t.Errorf("impl missing client-stream method, got:\n%s", impl)
	}
}

func TestGenGRPCRPC_Bidi(t *testing.T) {
	t.Chdir(t.TempDir())
	setupServiceForRPC(t)

	if err := GenGRPCRPC("Foo", "Chat", GenGRPCRPCOptions{Bidi: true}); err != nil {
		t.Fatalf("GenGRPCRPC: %v", err)
	}
	proto, _ := os.ReadFile(filepath.Join("api", "proto", "foo", "v1", "foo.proto"))
	if !strings.Contains(string(proto), "rpc Chat(stream ChatRequest) returns (stream ChatResponse);") {
		t.Errorf("proto missing bidi rpc, got:\n%s", proto)
	}
	impl, _ := os.ReadFile(filepath.Join("internal", "grpc", "services", "foo.go"))
	if !strings.Contains(string(impl), "func (s *FooService) Chat(stream foopb.FooService_ChatServer) error") {
		t.Errorf("impl missing bidi method, got:\n%s", impl)
	}
}

func TestGenGRPCRPC_MultipleStreamFlagsError(t *testing.T) {
	t.Chdir(t.TempDir())
	setupServiceForRPC(t)
	err := GenGRPCRPC("Foo", "Bad", GenGRPCRPCOptions{Stream: true, Bidi: true})
	if err == nil {
		t.Error("expected error when multiple stream flags set")
	}
}

func TestGenGRPCRPC_Idempotent(t *testing.T) {
	t.Chdir(t.TempDir())
	setupServiceForRPC(t)
	if err := GenGRPCRPC("Foo", "Hello", GenGRPCRPCOptions{}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := GenGRPCRPC("Foo", "Hello", GenGRPCRPCOptions{}); err != nil {
		t.Fatalf("second call should not error: %v", err)
	}
	proto, _ := os.ReadFile(filepath.Join("api", "proto", "foo", "v1", "foo.proto"))
	if strings.Count(string(proto), "rpc Hello(") != 1 {
		t.Errorf("expected exactly one Hello rpc, got:\n%s", proto)
	}
}

func TestGenGRPCRPC_MissingService(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")
	err := GenGRPCRPC("Nope", "Hello", GenGRPCRPCOptions{})
	if err == nil {
		t.Error("expected error when proto missing")
	}
}
