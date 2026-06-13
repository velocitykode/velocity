package console

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/velocitykode/velocity/console/scaffold"

	cli "github.com/velocitykode/velocity-cli"
	"github.com/velocitykode/velocity/console/stubs"
)

// MakeHandlerOptions holds flags for the make:handler command.
type MakeHandlerOptions struct {
	Resource bool
	API      bool
	Dir      string // --dir output root override (default internal/handlers); name-based nesting still applies under it
}

// MakeHandler generates a new handler file from a stub template.
func MakeHandler(name string, opts MakeHandlerOptions) error {
	// Handlers support namespaced names like "Admin/Users" that map to
	// nested output directories, so the slash-permitting validator applies.
	if err := scaffold.ValidateNestedName(name); err != nil {
		return err
	}

	handlerName := toHandlerName(name)

	packageName := "handlers"
	handlerRoot, err := scaffold.ResolveDir("internal/handlers", opts.Dir)
	if err != nil {
		return err
	}
	outputDir := handlerRoot

	if strings.Contains(name, "/") {
		parts := strings.Split(name, "/")
		handlerName = toHandlerName(parts[len(parts)-1])
		packageName = strings.ToLower(parts[len(parts)-2])
		for i := range parts[:len(parts)-1] {
			parts[i] = strings.ToLower(parts[i])
		}
		outputDir = filepath.Join(handlerRoot, filepath.Join(parts[:len(parts)-1]...))
	}

	// Defence in depth: even though scaffold.ValidateName rejects the known
	// traversal shapes, recompute and confirm the resolved directory still
	// lives inside the handler root before writing anything to disk.
	if err := scaffold.EnsureWithinRoot(handlerRoot, outputDir); err != nil {
		return fmt.Errorf("invalid handler name %q: %w", name, err)
	}

	// scaffold.EnsureWithinRoot is lexical only. The name-derived subdirectory (and the
	// --dir root) may include a pre-existing symlink that redirects the write
	// outside the tree, so re-run the symlink-component guard on the final,
	// fully-assembled output directory before creating or writing anything.
	if err := scaffold.EnsureNoSymlinkComponents(outputDir); err != nil {
		return fmt.Errorf("invalid handler output dir %q: %w", outputDir, err)
	}

	if err := os.MkdirAll(outputDir, defaultDirMode); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	filename := toSnakeCase(handlerName) + ".go"
	outputPath := filepath.Join(outputDir, filename)

	if err := scaffold.EnsureWritableTarget(outputPath, "handler"); err != nil {
		return err
	}

	stubContent, err := stubs.Get("internal/handlers/handler.go.stub")
	if err != nil {
		stubContent = []byte(`package {{ .Package }}

import "github.com/velocitykode/velocity/router"

func {{ .HandlerName }}(ctx *router.Context) error {
	return ctx.String(200, "{{ .HandlerName }}")
}
`)
	}

	tmpl, err := template.New("handler").Parse(string(stubContent))
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}

	data := map[string]interface{}{
		"Package":     packageName,
		"HandlerName": handlerName,
		"Resource":    opts.Resource,
		"API":         opts.API,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	if err := os.WriteFile(outputPath, buf.Bytes(), defaultFileMode); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	cli.Success(fmt.Sprintf("Created: %s", outputPath))
	return nil
}

func toHandlerName(name string) string {
	name = strings.TrimSuffix(name, "Handler")
	name = strings.TrimSuffix(name, "handler")
	name = strings.TrimSuffix(name, "Controller")
	name = strings.TrimSuffix(name, "controller")
	return toPascalCase(name)
}

func toPascalCase(s string) string {
	return scaffold.PascalCase(s)
}

// toSnakeCase delegates to scaffold.SnakeCase so first-party generators share
// the same file/table naming behavior without importing the ORM package.
func toSnakeCase(s string) string {
	return scaffold.SnakeCase(s)
}
