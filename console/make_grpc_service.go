package console

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	cli "github.com/velocitykode/velocity-cli"
	"github.com/velocitykode/velocity/console/stubs"
)

// MakeGRPCServiceOptions holds flags for the make:grpc:service command.
type MakeGRPCServiceOptions struct{}

// MakeGRPCService scaffolds a new gRPC service: proto file, server impl, and
// provider wiring. It is safe to call repeatedly with different service
// names. The provider is created once and subsequent calls inject
// registrations at the // vel:grpc:imports and // vel:grpc:services marker
// comments.
func MakeGRPCService(name string, opts MakeGRPCServiceOptions) error {
	if err := validateMakeName(name); err != nil {
		return err
	}

	serviceName := grpcServiceName(name)
	packageName := grpcPackageName(name)
	protoAlias := grpcProtoAlias(packageName)

	// Re-validate the derived package name. grpcPackageName lower-cases the
	// input but does not strip path separators or "..", so a sufficiently
	// crafted argument could still smuggle a traversal segment into the
	// derived directories built below.
	if err := validateMakeName(packageName); err != nil {
		return fmt.Errorf("derived package name %q from %q is unsafe: %w", packageName, name, err)
	}

	modulePath, err := readModulePath()
	if err != nil {
		return err
	}

	// Create buf configs FIRST so a write failure here does not leave a
	// proto on disk that blocks the user from rerunning. The previous
	// order (proto then configs) could lock the command into "proto
	// already exists" on every subsequent attempt after a config-write
	// failure, with no impl or provider wired.
	if err := ensureBufConfigs(); err != nil {
		return err
	}
	if err := writeProtoFile(packageName, serviceName, protoAlias, modulePath); err != nil {
		return err
	}
	if err := writeServiceImpl(packageName, serviceName, protoAlias, modulePath); err != nil {
		return err
	}
	if err := wireGRPCProvider(packageName, serviceName, protoAlias, modulePath); err != nil {
		return err
	}

	cli.Newline()
	cli.Muted("Next: vel make:grpc:gen  (generate Go code from .proto)")
	return nil
}

func writeProtoFile(packageName, serviceName, protoAlias, modulePath string) error {
	protoRoot := filepath.Join("api", "proto")
	dir := filepath.Join(protoRoot, packageName, "v1")
	if err := ensureWithinRoot(protoRoot, dir); err != nil {
		return fmt.Errorf("invalid package name %q: %w", packageName, err)
	}
	if err := os.MkdirAll(dir, defaultDirMode); err != nil {
		return fmt.Errorf("create proto dir: %w", err)
	}
	path := filepath.Join(dir, packageName+".proto")
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("proto already exists: %s", path)
	}

	stub, err := stubs.Get("grpc/proto.proto.stub")
	if err != nil {
		return fmt.Errorf("load proto stub: %w", err)
	}
	tmpl, err := template.New("proto").Parse(string(stub))
	if err != nil {
		return fmt.Errorf("parse proto stub: %w", err)
	}

	data := map[string]interface{}{
		"PackageName": packageName,
		"ServiceName": serviceName,
		"ModulePath":  modulePath,
		"RPCs":        []map[string]string{},
		"Messages":    []map[string]string{},
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("render proto: %w", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), defaultFileMode); err != nil {
		return fmt.Errorf("write proto: %w", err)
	}
	cli.Success(fmt.Sprintf("Created: %s", path))
	return nil
}

// ensureBufConfigs writes api/proto/buf.yaml and api/proto/buf.gen.yaml on
// first run so `vel make:grpc:gen` works out of the box. Existing files are
// left untouched. Write failures are propagated, since the scaffolder
// otherwise reports success while leaving generation broken.
func ensureBufConfigs() error {
	protoRoot := filepath.Join("api", "proto")
	if err := os.MkdirAll(protoRoot, defaultDirMode); err != nil {
		return fmt.Errorf("create %s: %w", protoRoot, err)
	}

	bufYaml := filepath.Join(protoRoot, "buf.yaml")
	if _, err := os.Stat(bufYaml); os.IsNotExist(err) {
		content := `version: v2
modules:
  - path: .
lint:
  use:
    - STANDARD
breaking:
  use:
    - FILE
`
		if err := os.WriteFile(bufYaml, []byte(content), defaultFileMode); err != nil {
			return fmt.Errorf("write %s: %w", bufYaml, err)
		}
		cli.Success(fmt.Sprintf("Created: %s", bufYaml))
	}

	bufGen := filepath.Join(protoRoot, "buf.gen.yaml")
	if _, err := os.Stat(bufGen); os.IsNotExist(err) {
		content := `version: v2
plugins:
  - remote: buf.build/protocolbuffers/go
    out: ../gen/go
    opt: paths=source_relative
  - remote: buf.build/grpc/go
    out: ../gen/go
    opt:
      - paths=source_relative
      - require_unimplemented_servers=false
`
		if err := os.WriteFile(bufGen, []byte(content), defaultFileMode); err != nil {
			return fmt.Errorf("write %s: %w", bufGen, err)
		}
		cli.Success(fmt.Sprintf("Created: %s", bufGen))
	}
	return nil
}

func writeServiceImpl(packageName, serviceName, protoAlias, modulePath string) error {
	dir := filepath.Join("internal", "grpc", "services")
	if err := os.MkdirAll(dir, defaultDirMode); err != nil {
		return fmt.Errorf("create services dir: %w", err)
	}
	path := filepath.Join(dir, packageName+".go")
	if err := ensureWithinRoot(dir, path); err != nil {
		return fmt.Errorf("invalid package name %q: %w", packageName, err)
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("service already exists: %s", path)
	}

	stub, err := stubs.Get("grpc/service.go.stub")
	if err != nil {
		return fmt.Errorf("load service stub: %w", err)
	}
	tmpl, err := template.New("service").Parse(string(stub))
	if err != nil {
		return fmt.Errorf("parse service stub: %w", err)
	}

	data := map[string]interface{}{
		"PackageName": packageName,
		"ServiceName": serviceName,
		"ProtoAlias":  protoAlias,
		"ModulePath":  modulePath,
		"Methods":     []map[string]string{},
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("render service: %w", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), defaultFileMode); err != nil {
		return fmt.Errorf("write service: %w", err)
	}
	cli.Success(fmt.Sprintf("Created: %s", path))
	return nil
}
