package console

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/velocitykode/velocity/console/scaffold"
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

	filename := toSnakeCase(handlerName) + ".go"

	data := map[string]interface{}{
		"Package":     packageName,
		"HandlerName": handlerName,
		"Resource":    opts.Resource,
		"API":         opts.API,
	}

	// Route the write through the shared generator. The name-derived nested
	// directory is passed as the resolved default dir (no further override),
	// and handlerName is a validated PascalCase segment so the generator's
	// own ValidateName accepts it. A missing embedded stub hard-fails.
	return writeScaffoldedFile(handlerName, "", outputDir, "handler", filename, "internal/handlers/handler.go.stub", data)
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
