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

// MakeMiddlewareOptions holds flags for the make:middleware command.
type MakeMiddlewareOptions struct{}

// MakeMiddleware generates a new middleware file from a stub template.
func MakeMiddleware(name string, opts MakeMiddlewareOptions) error {
	if err := validateMakeName(name); err != nil {
		return err
	}

	middlewareName := toMiddlewareName(name)

	outputDir := "internal/middleware"
	if err := os.MkdirAll(outputDir, defaultDirMode); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	filename := toSnakeCase(middlewareName) + ".go"
	outputPath := filepath.Join(outputDir, filename)
	if err := ensureWithinRoot(outputDir, outputPath); err != nil {
		return fmt.Errorf("invalid middleware name %q: %w", name, err)
	}

	if _, err := os.Stat(outputPath); err == nil {
		return fmt.Errorf("middleware already exists: %s", outputPath)
	}

	stubContent, err := stubs.Get("internal/middleware/generated.go.stub")
	if err != nil {
		stubContent = []byte(`package {{ .Package }}

import "github.com/velocitykode/velocity/router"

// {{ .Name }} middleware
func {{ .Name }}(next router.HandlerFunc) router.HandlerFunc {
	return func(ctx *router.Context) error {
		// Before request
		return next(ctx)
	}
}
`)
	}

	tmpl, err := template.New("middleware").Parse(string(stubContent))
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}

	data := map[string]interface{}{
		"Package": "middleware",
		"Name":    middlewareName,
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

func toMiddlewareName(name string) string {
	name = strings.TrimSuffix(name, "Middleware")
	name = strings.TrimSuffix(name, "middleware")
	return toPascalCase(name)
}
