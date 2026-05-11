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
	if err := MakeGRPCService("Foo", MakeGRPCServiceOptions{}); err != nil {
		t.Fatalf("seed service: %v", err)
	}
}

func TestMakeGRPCRPC_UnaryAddsRpcAndMethod(t *testing.T) {
	t.Chdir(t.TempDir())
	setupServiceForRPC(t)

	if err := MakeGRPCRPC("Foo", "Hello", MakeGRPCRPCOptions{}); err != nil {
		t.Fatalf("MakeGRPCRPC: %v", err)
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
	if !strings.Contains(string(impl), "func (s *FooService) Hello(ctx context.Context, req *foov1.HelloRequest) (*foov1.HelloResponse, error)") {
		t.Errorf("impl missing unary Hello method, got:\n%s", impl)
	}
}

func TestMakeGRPCRPC_ServerStream(t *testing.T) {
	t.Chdir(t.TempDir())
	setupServiceForRPC(t)

	if err := MakeGRPCRPC("Foo", "Tail", MakeGRPCRPCOptions{Stream: true}); err != nil {
		t.Fatalf("MakeGRPCRPC: %v", err)
	}
	proto, _ := os.ReadFile(filepath.Join("api", "proto", "foo", "v1", "foo.proto"))
	if !strings.Contains(string(proto), "rpc Tail(TailRequest) returns (stream TailResponse);") {
		t.Errorf("proto missing server-stream rpc, got:\n%s", proto)
	}
	impl, _ := os.ReadFile(filepath.Join("internal", "grpc", "services", "foo.go"))
	if !strings.Contains(string(impl), "func (s *FooService) Tail(req *foov1.TailRequest, stream foov1.FooService_TailServer) error") {
		t.Errorf("impl missing server-stream method, got:\n%s", impl)
	}
}

func TestMakeGRPCRPC_ClientStream(t *testing.T) {
	t.Chdir(t.TempDir())
	setupServiceForRPC(t)

	if err := MakeGRPCRPC("Foo", "Upload", MakeGRPCRPCOptions{ClientStream: true}); err != nil {
		t.Fatalf("MakeGRPCRPC: %v", err)
	}
	proto, _ := os.ReadFile(filepath.Join("api", "proto", "foo", "v1", "foo.proto"))
	if !strings.Contains(string(proto), "rpc Upload(stream UploadRequest) returns (UploadResponse);") {
		t.Errorf("proto missing client-stream rpc, got:\n%s", proto)
	}
	impl, _ := os.ReadFile(filepath.Join("internal", "grpc", "services", "foo.go"))
	if !strings.Contains(string(impl), "func (s *FooService) Upload(stream foov1.FooService_UploadServer) error") {
		t.Errorf("impl missing client-stream method, got:\n%s", impl)
	}
}

func TestMakeGRPCRPC_Bidi(t *testing.T) {
	t.Chdir(t.TempDir())
	setupServiceForRPC(t)

	if err := MakeGRPCRPC("Foo", "Chat", MakeGRPCRPCOptions{Bidi: true}); err != nil {
		t.Fatalf("MakeGRPCRPC: %v", err)
	}
	proto, _ := os.ReadFile(filepath.Join("api", "proto", "foo", "v1", "foo.proto"))
	if !strings.Contains(string(proto), "rpc Chat(stream ChatRequest) returns (stream ChatResponse);") {
		t.Errorf("proto missing bidi rpc, got:\n%s", proto)
	}
	impl, _ := os.ReadFile(filepath.Join("internal", "grpc", "services", "foo.go"))
	if !strings.Contains(string(impl), "func (s *FooService) Chat(stream foov1.FooService_ChatServer) error") {
		t.Errorf("impl missing bidi method, got:\n%s", impl)
	}
}

func TestMakeGRPCRPC_MultipleStreamFlagsError(t *testing.T) {
	t.Chdir(t.TempDir())
	setupServiceForRPC(t)
	err := MakeGRPCRPC("Foo", "Bad", MakeGRPCRPCOptions{Stream: true, Bidi: true})
	if err == nil {
		t.Error("expected error when multiple stream flags set")
	}
}

func TestMakeGRPCRPC_Idempotent(t *testing.T) {
	t.Chdir(t.TempDir())
	setupServiceForRPC(t)
	if err := MakeGRPCRPC("Foo", "Hello", MakeGRPCRPCOptions{}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := MakeGRPCRPC("Foo", "Hello", MakeGRPCRPCOptions{}); err != nil {
		t.Fatalf("second call should not error: %v", err)
	}
	proto, _ := os.ReadFile(filepath.Join("api", "proto", "foo", "v1", "foo.proto"))
	if strings.Count(string(proto), "rpc Hello(") != 1 {
		t.Errorf("expected exactly one Hello rpc, got:\n%s", proto)
	}
}

func TestMakeGRPCRPC_MissingService(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")
	err := MakeGRPCRPC("Nope", "Hello", MakeGRPCRPCOptions{})
	if err == nil {
		t.Error("expected error when proto missing")
	}
}
