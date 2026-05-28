package console

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	cli "github.com/velocitykode/velocity-cli"
	"github.com/velocitykode/velocity/console/stubs"
)

const (
	grpcImportsMarker  = "// vel:grpc:imports"
	grpcServicesMarker = "// vel:grpc:services"
)

// wireGRPCProvider creates internal/providers/grpc_provider.go if missing or
// injects a new service registration at the marker comments if it exists.
// When the file exists without markers, a manual wire snippet is printed
// instead of mutating user code.
func wireGRPCProvider(packageName, serviceName, protoAlias, modulePath string) error {
	dir := filepath.Join("internal", "providers")
	if err := os.MkdirAll(dir, defaultDirMode); err != nil {
		return fmt.Errorf("create providers dir: %w", err)
	}
	path := filepath.Join(dir, "grpc_provider.go")

	varName := strings.ToLower(packageName)

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return writeNewGRPCProvider(path, packageName, serviceName, protoAlias, modulePath, varName)
	}

	return injectGRPCServiceRegistration(path, packageName, serviceName, protoAlias, modulePath, varName)
}

func writeNewGRPCProvider(path, packageName, serviceName, protoAlias, modulePath, varName string) error {
	stub, err := stubs.Get("grpc/provider.go.stub")
	if err != nil {
		return fmt.Errorf("load provider stub: %w", err)
	}
	tmpl, err := template.New("provider").Parse(string(stub))
	if err != nil {
		return fmt.Errorf("parse provider stub: %w", err)
	}
	data := map[string]string{
		"PackageName": packageName,
		"ServiceName": serviceName,
		"ProtoAlias":  protoAlias,
		"ModulePath":  modulePath,
		"VarName":     varName,
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("render provider: %w", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), defaultFileMode); err != nil {
		return fmt.Errorf("write provider: %w", err)
	}
	cli.Success(fmt.Sprintf("Created: %s", path))
	cli.Muted("  Register in internal/app/bootstrap.go: providers.GRPCProvider{}")
	return nil
}

func injectGRPCServiceRegistration(path, packageName, serviceName, protoAlias, modulePath, varName string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read provider: %w", err)
	}
	content := string(raw)

	if strings.Contains(content, fmt.Sprintf("Register%sServer(", serviceName)) {
		cli.Muted(fmt.Sprintf("Provider already registers %s, skipping wire", serviceName))
		return nil
	}

	if !strings.Contains(content, grpcImportsMarker) || !strings.Contains(content, grpcServicesMarker) {
		cli.Muted("grpc_provider.go missing markers; add the following manually:")
		cli.Muted(fmt.Sprintf("  import: %s \"%s/api/gen/go/%s/v1\"", protoAlias, modulePath, packageName))
		cli.Muted(fmt.Sprintf("  in Register(): %s := services.New%s()", varName, serviceName))
		cli.Muted("                 p.server.RegisterService(func(srv interface{}) {")
		cli.Muted(fmt.Sprintf("                     %s.Register%sServer(srv.(*googleGrpc.Server), %s)", protoAlias, serviceName, varName))
		cli.Muted("                 })")
		return nil
	}

	importLine := fmt.Sprintf("\t%s \"%s/api/gen/go/%s/v1\"", protoAlias, modulePath, packageName)
	content = injectAfterMarker(content, grpcImportsMarker, importLine)

	regBlock := strings.Join([]string{
		fmt.Sprintf("\t%s := services.New%s()", varName, serviceName),
		"\tp.server.RegisterService(func(srv interface{}) {",
		fmt.Sprintf("\t\t%s.Register%sServer(srv.(*googleGrpc.Server), %s)", protoAlias, serviceName, varName),
		"\t})",
		"",
	}, "\n")
	content = injectAfterMarker(content, grpcServicesMarker, regBlock)

	if err := os.WriteFile(path, []byte(content), defaultFileMode); err != nil {
		return fmt.Errorf("write provider: %w", err)
	}
	cli.Success(fmt.Sprintf("Wired: %s", path))
	return nil
}

// injectAfterMarker inserts injectLine immediately after the first line that
// contains marker, preserving the marker. The injected text keeps the file's
// indentation since the caller supplies tabs.
func injectAfterMarker(content, marker, injectLine string) string {
	lines := strings.Split(content, "\n")
	for i, ln := range lines {
		if strings.Contains(ln, marker) {
			out := append([]string{}, lines[:i+1]...)
			out = append(out, injectLine)
			out = append(out, lines[i+1:]...)
			return strings.Join(out, "\n")
		}
	}
	return content
}
