package console

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	cli "github.com/velocitykode/velocity-cli"
)

// MakeGRPCRPCOptions selects the streaming variant for the new rpc. At most
// one of Stream / ClientStream / Bidi may be set; multiple are an error.
type MakeGRPCRPCOptions struct {
	Stream       bool
	ClientStream bool
	Bidi         bool
}

func (o MakeGRPCRPCOptions) kind() (grpcRPCKind, error) {
	count := 0
	kind := grpcRPCUnary
	if o.Stream {
		count++
		kind = grpcRPCServerStream
	}
	if o.ClientStream {
		count++
		kind = grpcRPCClientStream
	}
	if o.Bidi {
		count++
		kind = grpcRPCBidi
	}
	if count > 1 {
		return 0, fmt.Errorf("only one of --stream, --client-stream, --bidi may be set")
	}
	return kind, nil
}

// MakeGRPCRPC appends a new rpc to an existing service's proto file and adds
// a matching method stub on the Go impl. The service must already exist;
// run `vel make:grpc:service <Name>` first.
func MakeGRPCRPC(serviceArg, rpcArg string, opts MakeGRPCRPCOptions) error {
	kind, err := opts.kind()
	if err != nil {
		return err
	}

	if err := validateMakeName(serviceArg); err != nil {
		return fmt.Errorf("service argument: %w", err)
	}
	if err := validateMakeName(rpcArg); err != nil {
		return fmt.Errorf("rpc argument: %w", err)
	}

	serviceName := grpcServiceName(serviceArg)
	packageName := grpcPackageName(serviceArg)
	protoAlias := grpcProtoAlias(packageName)
	rpcName := toPascalCase(rpcArg)

	// Re-validate the derived package name. grpcPackageName lower-cases
	// the input but does not strip "/" or "..", so a sufficiently crafted
	// argument could still smuggle a traversal segment into the proto and
	// impl paths constructed below.
	if err := validateMakeName(packageName); err != nil {
		return fmt.Errorf("derived package name %q from %q is unsafe: %w", packageName, serviceArg, err)
	}

	protoRoot := filepath.Join("api", "proto")
	protoPath := filepath.Join(protoRoot, packageName, "v1", packageName+".proto")
	if err := ensureWithinRoot(protoRoot, protoPath); err != nil {
		return fmt.Errorf("invalid service name %q: %w", serviceArg, err)
	}
	if _, err := os.Stat(protoPath); os.IsNotExist(err) {
		return fmt.Errorf("proto not found: %s (run `vel make:grpc:service %s` first)", protoPath, serviceName)
	}

	implRoot := filepath.Join("internal", "grpc", "services")
	implPath := filepath.Join(implRoot, packageName+".go")
	if err := ensureWithinRoot(implRoot, implPath); err != nil {
		return fmt.Errorf("invalid service name %q: %w", serviceArg, err)
	}
	if _, err := os.Stat(implPath); os.IsNotExist(err) {
		return fmt.Errorf("service impl not found: %s", implPath)
	}

	if err := appendRPCToProto(protoPath, serviceName, rpcName, kind); err != nil {
		return err
	}
	if err := appendMethodToImpl(implPath, serviceName, rpcName, protoAlias, kind); err != nil {
		return err
	}
	return nil
}

// appendRPCToProto inserts an rpc line before the closing brace of the
// service block and appends empty Request/Response messages at end of file.
// Idempotent: if an rpc with the same name already exists, no changes.
func appendRPCToProto(path, serviceName, rpcName string, kind grpcRPCKind) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read proto: %w", err)
	}
	content := string(raw)

	rpcSig := fmt.Sprintf("rpc %s(", rpcName)
	if strings.Contains(content, rpcSig) {
		cli.Muted(fmt.Sprintf("rpc %s already exists in %s, skipping", rpcName, path))
		return nil
	}

	openIdx, closeIdx, err := findServiceBlock(content, serviceName)
	if err != nil {
		return fmt.Errorf("%s in %s", err.Error(), path)
	}

	rpcLine := "  " + protoRPCLine(rpcName, kind)
	// Insert immediately before the closing brace of the service block.
	// findServiceBlock returns closeIdx pointing at the matching '}'; we
	// strip any trailing whitespace on the preceding line so the inserted
	// rpc lines up with existing rpc declarations.
	insertAt := closeIdx
	for insertAt > openIdx && (content[insertAt-1] == ' ' || content[insertAt-1] == '\t') {
		insertAt--
	}
	updated := content[:insertAt] + rpcLine + "\n" + content[insertAt:]

	msgBlock := fmt.Sprintf("\nmessage %sRequest {\n}\n\nmessage %sResponse {\n}\n", rpcName, rpcName)
	if !strings.HasSuffix(updated, "\n") {
		updated += "\n"
	}
	updated += msgBlock

	if err := os.WriteFile(path, []byte(updated), 0644); err != nil {
		return fmt.Errorf("write proto: %w", err)
	}
	cli.Success(fmt.Sprintf("Added rpc %s to %s", rpcName, path))
	return nil
}

// appendMethodToImpl appends a method stub matching the rpc kind. Imports
// "context" are added when needed (unary only). Idempotent on rpc name.
func appendMethodToImpl(path, serviceName, rpcName, protoAlias string, kind grpcRPCKind) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read impl: %w", err)
	}
	content := string(raw)

	methodMarker := fmt.Sprintf(") %s(", rpcName)
	if strings.Contains(content, methodMarker) {
		cli.Muted(fmt.Sprintf("method %s already exists in %s, skipping", rpcName, path))
		return nil
	}

	signature := goMethodSignature(serviceName, rpcName, protoAlias, kind)
	body := goMethodBody(kind)

	if kind == grpcRPCUnary && !strings.Contains(content, `"context"`) {
		content = ensureContextImport(content)
	}

	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += "\n" + signature + " {\n" + body + "\n}\n"

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Errorf("write impl: %w", err)
	}
	cli.Success(fmt.Sprintf("Added method %s to %s", rpcName, path))
	return nil
}

// findServiceBlock locates the body of `service <name> { ... }` and returns
// the byte offsets of the opening '{' and the matching closing '}'. Both
// the header search AND the brace counting respect // line comments, /*
// block */ comments, and "..." string literals so that:
//
//  1. an rpc option block like `rpc Get(...) returns (...) { option ...; }`
//     inside the service does not terminate the scan prematurely; and
//  2. a commented-out service header (e.g. `// service FooService {`) or a
//     service-shaped fragment in a string literal does not capture the scan
//     ahead of the real declaration.
//
// The previous regex-based header search picked up the first textual match
// regardless of context, which let a stray comment or string anchor the
// brace counter to the wrong offset and corrupt the proto.
func findServiceBlock(content, serviceName string) (openIdx, closeIdx int, err error) {
	i := skipNonCode(content, 0)
	for i < len(content) {
		if isIdentAt(content, i, "service") {
			// Between `service`, the name, and `{` proto permits any
			// run of whitespace AND comments (line + block). Using
			// skipNonCode here lets us match valid declarations like
			//   service /* docs */ FooService { ... }
			//   service FooService // docs
			//                      { ... }
			// which the previous space-only skip would have missed.
			j := skipNonCode(content, i+len("service"))
			if isIdentAt(content, j, serviceName) {
				k := skipNonCode(content, j+len(serviceName))
				if k < len(content) && content[k] == '{' {
					return scanBraces(content, k, serviceName)
				}
			}
		}
		i++
		i = skipNonCode(content, i)
	}
	return 0, 0, fmt.Errorf("service %s not found", serviceName)
}

// skipNonCode advances past any run of whitespace, // line comments, /*
// block */ comments, and "..." string literals starting at i. It returns
// the next index that is not inside a comment or string (or len(content)
// if the file ends inside one).
func skipNonCode(content string, i int) int {
	for i < len(content) {
		c := content[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '/' && i+1 < len(content) && content[i+1] == '/':
			for i < len(content) && content[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(content) && content[i+1] == '*':
			i += 2
			for i+1 < len(content) && !(content[i] == '*' && content[i+1] == '/') {
				i++
			}
			if i+1 < len(content) {
				i += 2
			} else {
				i = len(content)
			}
		case c == '"':
			i++
			for i < len(content) && content[i] != '"' {
				if content[i] == '\\' && i+1 < len(content) {
					i += 2
					continue
				}
				i++
			}
			if i < len(content) {
				i++
			}
		default:
			return i
		}
	}
	return i
}

// isIdentAt reports whether ident appears at content[i:] as a complete
// identifier, i.e. preceded and followed by a non-identifier character (or
// a boundary). This prevents `service` from matching inside `serviceFoo`
// and `FooService` from matching inside `FooServiceV2`.
func isIdentAt(content string, i int, ident string) bool {
	if i+len(ident) > len(content) {
		return false
	}
	if content[i:i+len(ident)] != ident {
		return false
	}
	if i > 0 && isIdentRune(content[i-1]) {
		return false
	}
	if i+len(ident) < len(content) && isIdentRune(content[i+len(ident)]) {
		return false
	}
	return true
}

func isIdentRune(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

// scanBraces walks content starting at openBraceIdx (which must point at
// '{') and returns the matching close '}' offset, respecting comments and
// string literals. The serviceName is used only for the error message.
func scanBraces(content string, openBraceIdx int, serviceName string) (openIdx, closeIdx int, err error) {
	depth := 1
	i := openBraceIdx + 1
	for i < len(content) {
		c := content[i]
		switch {
		case c == '/' && i+1 < len(content) && content[i+1] == '/':
			for i < len(content) && content[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(content) && content[i+1] == '*':
			i += 2
			for i+1 < len(content) && !(content[i] == '*' && content[i+1] == '/') {
				i++
			}
			if i+1 < len(content) {
				i += 2
			} else {
				i = len(content)
			}
		case c == '"':
			i++
			for i < len(content) && content[i] != '"' {
				if content[i] == '\\' && i+1 < len(content) {
					i += 2
					continue
				}
				i++
			}
			if i < len(content) {
				i++
			}
		case c == '{':
			depth++
			i++
		case c == '}':
			depth--
			if depth == 0 {
				return openBraceIdx, i, nil
			}
			i++
		default:
			i++
		}
	}
	return 0, 0, fmt.Errorf("service %s has unbalanced braces", serviceName)
}

// ensureContextImport adds "context" to an existing import block. If the
// file has only a single import line (no block), it is rewritten as a block.
func ensureContextImport(content string) string {
	blockRe := regexp.MustCompile(`(?ms)import\s*\(\s*(.*?)\s*\)`)
	if m := blockRe.FindStringSubmatchIndex(content); m != nil {
		bodyStart := m[2]
		return content[:bodyStart] + "\"context\"\n\t" + content[bodyStart:]
	}
	singleRe := regexp.MustCompile(`(?m)^import\s+"([^"]+)"\s*$`)
	if m := singleRe.FindStringSubmatchIndex(content); m != nil {
		old := content[m[0]:m[1]]
		pkg := content[m[2]:m[3]]
		replacement := fmt.Sprintf("import (\n\t\"context\"\n\n\t\"%s\"\n)", pkg)
		return strings.Replace(content, old, replacement, 1)
	}
	return content
}
