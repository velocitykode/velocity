package console

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// readModulePath returns the module path declared in go.mod in the current
// working directory. It is used to construct import paths for generated
// gRPC code (e.g. "<module>/api/gen/go/foo/v1").
func readModulePath() (string, error) {
	f, err := os.Open("go.mod")
	if err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module")), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("module directive not found in go.mod")
}

// grpcServiceName normalises a user-supplied service identifier into the Go
// type name we use in generated code (e.g. "foo" / "FooService" / "fooService"
// → "FooService"). The proto file uses the same identifier.
func grpcServiceName(name string) string {
	name = strings.TrimSuffix(name, "Service")
	name = strings.TrimSuffix(name, "service")
	return toPascalCase(name) + "Service"
}

// grpcPackageName returns the lowercase proto/Go package segment for a
// service (e.g. "FooService" → "foo"). This is also the directory name
// under api/proto/ and api/gen/go/.
func grpcPackageName(name string) string {
	name = strings.TrimSuffix(name, "Service")
	name = strings.TrimSuffix(name, "service")
	return strings.ToLower(toPascalCase(name))
}

// grpcBaseName returns the PascalCase service base with any "Service" suffix
// stripped (e.g. "fooService" / "Foo" → "Foo", "template_control" →
// "TemplateControl"). It is the seed for the generated file names: the proto
// file is the lower-cased base and the impl file is its snake_case form.
func grpcBaseName(name string) string {
	name = strings.TrimSuffix(name, "Service")
	name = strings.TrimSuffix(name, "service")
	return toPascalCase(name)
}

// grpcProtoAlias returns the default import alias used at the call site for
// the generated proto package (e.g. "foo" → "foopb"). The generated package
// itself is named "<leaf>v1" via the proto go_package option; the alias is
// purely the local name code refers to it by, and the house convention is
// "<leaf>pb". A caller can override it with the --alias flag on
// make:grpc:service.
func grpcProtoAlias(packageName string) string {
	return packageName + "pb"
}

// grpcRPCKind describes whether an rpc is unary or one of the three
// streaming variants. The zero value is unary.
type grpcRPCKind int

const (
	grpcRPCUnary grpcRPCKind = iota
	grpcRPCServerStream
	grpcRPCClientStream
	grpcRPCBidi
)

// protoRPCLine renders the proto rpc declaration for the given kind.
func protoRPCLine(rpcName string, kind grpcRPCKind) string {
	req := rpcName + "Request"
	resp := rpcName + "Response"
	switch kind {
	case grpcRPCServerStream:
		return fmt.Sprintf("rpc %s(%s) returns (stream %s);", rpcName, req, resp)
	case grpcRPCClientStream:
		return fmt.Sprintf("rpc %s(stream %s) returns (%s);", rpcName, req, resp)
	case grpcRPCBidi:
		return fmt.Sprintf("rpc %s(stream %s) returns (stream %s);", rpcName, req, resp)
	default:
		return fmt.Sprintf("rpc %s(%s) returns (%s);", rpcName, req, resp)
	}
}

// goMethodSignature returns the Go method signature for the impl receiver
// given the rpc name and kind. The receiver is fixed to "s *<Service>".
func goMethodSignature(serviceName, rpcName, protoAlias string, kind grpcRPCKind) string {
	req := rpcName + "Request"
	resp := rpcName + "Response"
	switch kind {
	case grpcRPCServerStream:
		return fmt.Sprintf("func (s *%s) %s(req *%s.%s, stream %s.%s_%sServer) error",
			serviceName, rpcName, protoAlias, req, protoAlias, serviceName, rpcName)
	case grpcRPCClientStream:
		return fmt.Sprintf("func (s *%s) %s(stream %s.%s_%sServer) error",
			serviceName, rpcName, protoAlias, serviceName, rpcName)
	case grpcRPCBidi:
		return fmt.Sprintf("func (s *%s) %s(stream %s.%s_%sServer) error",
			serviceName, rpcName, protoAlias, serviceName, rpcName)
	default:
		return fmt.Sprintf("func (s *%s) %s(ctx context.Context, req *%s.%s) (*%s.%s, error)",
			serviceName, rpcName, protoAlias, req, protoAlias, resp)
	}
}

// goMethodBody returns the placeholder method body matching the kind.
func goMethodBody(kind grpcRPCKind) string {
	switch kind {
	case grpcRPCServerStream, grpcRPCClientStream, grpcRPCBidi:
		return "\treturn nil"
	default:
		return "\treturn nil, nil"
	}
}
