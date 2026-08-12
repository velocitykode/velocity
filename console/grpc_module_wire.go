package console

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"

	"github.com/velocitykode/prism"
	"github.com/velocitykode/velocity/console/stubs"
)

const (
	grpcImportsMarker  = "// vel:grpc:imports"
	grpcServicesMarker = "// vel:grpc:services"
)

// grpcModulePath is the fixed location of the generated module.
func grpcModulePath() string {
	return filepath.Join("internal", "modules", "grpc_module.go")
}

// preflightModuleWiring rejects an unsupported module wire before any
// proto/impl files are written, so a failure leaves the working tree clean and
// the suggested remedy (re-run with --no-module) is actually runnable. It
// only fails on the one predictable precondition wireGRPCModule cannot honor:
// an existing, marker-bearing module whose single `services` import does not
// match this service's impl dir. All other cases (no module yet, no markers,
// already registered, matching dir) are wireable and pass.
func preflightModuleWiring(sc grpcScaffold) error {
	if sc.NoModule {
		return nil
	}
	raw, err := os.ReadFile(grpcModulePath())
	if os.IsNotExist(err) {
		return nil // first service creates the module fresh
	}
	if err != nil {
		return fmt.Errorf("read module: %w", err)
	}
	return checkModuleServicesImport(string(raw), sc)
}

// checkModuleServicesImport returns the services-dir mismatch error when, and
// only when, the existing module would actually be mutated (markers present,
// service not already registered) but imports a different impl package.
func checkModuleServicesImport(content string, sc grpcScaffold) error {
	if !strings.Contains(content, grpcImportsMarker) || !strings.Contains(content, grpcServicesMarker) {
		return nil // no markers: a manual snippet is printed, nothing is mutated
	}
	if strings.Contains(content, fmt.Sprintf("Register%sServer(", sc.ServiceName)) {
		return nil // already registered: wire is a no-op
	}
	if sc.ServicesImport != "" && !strings.Contains(content, strconv.Quote(sc.ServicesImport)) {
		return fmt.Errorf("module %s imports a different services package than %q; "+
			"re-run %s with --no-module and wire it manually", grpcModulePath(), sc.ServicesImport, sc.ServiceName)
	}
	return nil
}

// wireGRPCModule creates internal/modules/grpc_module.go if missing or
// injects a new service registration at the marker comments if it exists.
// When the file exists without markers, a manual wire snippet is printed
// instead of mutating user code.
func wireGRPCModule(sc grpcScaffold) error {
	path := grpcModulePath()
	if err := os.MkdirAll(filepath.Dir(path), defaultDirMode); err != nil {
		return fmt.Errorf("create module dir: %w", err)
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return writeNewGRPCModule(path, sc)
	}

	return injectGRPCServiceRegistration(path, sc)
}

func writeNewGRPCModule(path string, sc grpcScaffold) error {
	stub, err := stubs.Get("grpc/module.go.stub")
	if err != nil {
		return fmt.Errorf("load module stub: %w", err)
	}
	tmpl, err := template.New("module").Parse(string(stub))
	if err != nil {
		return fmt.Errorf("parse module stub: %w", err)
	}
	data := map[string]string{
		"Alias":          sc.Alias,
		"ServiceName":    sc.ServiceName,
		"Leaf":           sc.Leaf,
		"Version":        sc.Version,
		"ModulePath":     sc.ModulePath,
		"ServicesImport": sc.ServicesImport,
		"VarName":        sc.VarName,
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("render module: %w", err)
	}
	if err := writeFormattedGo(path, buf.Bytes()); err != nil {
		return fmt.Errorf("write module: %w", err)
	}
	prism.Success(fmt.Sprintf("Created: %s", path))
	prism.Muted("  Register in internal/app/bootstrap.go: &modules.GRPCModule{}")
	return nil
}

func injectGRPCServiceRegistration(path string, sc grpcScaffold) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read module: %w", err)
	}
	content := string(raw)

	if strings.Contains(content, fmt.Sprintf("Register%sServer(", sc.ServiceName)) {
		prism.Muted(fmt.Sprintf("Module already registers %s, skipping wire", sc.ServiceName))
		return nil
	}

	importPath := fmt.Sprintf("%s/api/gen/go/%s/%s", sc.ModulePath, sc.Leaf, sc.Version)

	if !strings.Contains(content, grpcImportsMarker) || !strings.Contains(content, grpcServicesMarker) {
		prism.Muted("grpc_module.go missing markers; add the following manually:")
		prism.Muted(fmt.Sprintf("  import: %s \"%s\"", sc.Alias, importPath))
		prism.Muted(fmt.Sprintf("  in Init(): %s := services.New%s()", sc.VarName, sc.ServiceName))
		prism.Muted("                 p.server.RegisterService(func(srv interface{}) {")
		prism.Muted(fmt.Sprintf("                     %s.Register%sServer(srv.(*googleGrpc.Server), %s)", sc.Alias, sc.ServiceName, sc.VarName))
		prism.Muted("                 })")
		return nil
	}

	// The module imports exactly one impl package (the generated `services`
	// package). Auto-wiring cannot safely register a service whose impl lives
	// in a different directory: the registration calls services.NewX() against
	// that single import, and adding a second `services` package would clash.
	// GenGRPCService preflights this before writing files; the check is
	// repeated here as the last line of defense for direct callers.
	if err := checkModuleServicesImport(content, sc); err != nil {
		return err
	}

	// Reuse the alias already bound to this generated package when it is
	// already imported (two services sharing a --package leaf). Emitting a
	// second import for the same path would be a duplicate, and emitting the
	// new --alias while skipping the import would reference an undefined name.
	regAlias := sc.Alias
	if existing, ok := existingImportAlias(content, importPath, sc.GenPkgName); ok {
		if existing != sc.Alias {
			prism.Muted(fmt.Sprintf("module already imports %s as %s; reusing that alias", importPath, existing))
		}
		regAlias = existing
	} else {
		importLine := fmt.Sprintf("\t%s %s", sc.Alias, strconv.Quote(importPath))
		content = injectAfterMarker(content, grpcImportsMarker, importLine)
	}

	regBlock := strings.Join([]string{
		fmt.Sprintf("\t%s := services.New%s()", sc.VarName, sc.ServiceName),
		"\tp.server.RegisterService(func(srv interface{}) {",
		fmt.Sprintf("\t\t%s.Register%sServer(srv.(*googleGrpc.Server), %s)", regAlias, sc.ServiceName, sc.VarName),
		"\t})",
		"",
	}, "\n")
	content = injectAfterMarker(content, grpcServicesMarker, regBlock)

	if err := writeFormattedGo(path, []byte(content)); err != nil {
		return fmt.Errorf("write module: %w", err)
	}
	prism.Success(fmt.Sprintf("Wired: %s", path))
	return nil
}

// existingImportAlias reports the local name an already-present import of
// importPath is bound to, and whether the import was found. An aliased import
// (`adminpb "…/admin/v1"`) returns its alias; an unaliased import returns
// genPkgName, the package's own declared name, which is how call sites refer
// to it.
func existingImportAlias(content, importPath, genPkgName string) (string, bool) {
	quoted := strconv.Quote(importPath)
	for _, ln := range strings.Split(content, "\n") {
		t := strings.TrimSpace(ln)
		if !strings.HasSuffix(t, quoted) {
			continue
		}
		if prefix := strings.TrimSpace(strings.TrimSuffix(t, quoted)); prefix != "" {
			return prefix, true
		}
		return genPkgName, true
	}
	return "", false
}

// writeFormattedGo gofmt-formats src before writing it to path so the
// generated/mutated module stays canonical (notably keeping the injected
// imports sorted). If formatting fails (e.g. a user hand-edit left the file
// unparseable) the original bytes are written so the wire still lands.
func writeFormattedGo(path string, src []byte) error {
	out := src
	if formatted, err := format.Source(src); err == nil {
		out = formatted
	}
	return os.WriteFile(path, out, defaultFileMode)
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
