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

// MakeResourceOptions holds flags for the make:resource command.
type MakeResourceOptions struct{}

// MakeResource generates a new resource file from a stub template.
func MakeResource(name string, opts MakeResourceOptions) error {
	resourceName := toResourceName(name)

	outputDir := "internal/resources"
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	filename := toSnakeCase(resourceName) + ".go"
	outputPath := filepath.Join(outputDir, filename)

	if _, err := os.Stat(outputPath); err == nil {
		return fmt.Errorf("resource already exists: %s", outputPath)
	}

	stubContent, err := stubs.Get("internal/resources/resource.go.stub")
	if err != nil {
		stubContent = []byte(`package {{ .Package }}

// {{ .Name }}Resource transforms a {{ .Name }} into an API response.
// Add fields from the {{ .Name }} model to control what gets serialized.
type {{ .Name }}Resource struct {
}

// ToResource returns the resource as a map for JSON serialization.
// Map the model's fields into this response body.
func (r {{ .Name }}Resource) ToResource() map[string]any {
	return map[string]any{}
}
`)
	}

	tmpl, err := template.New("resource").Parse(string(stubContent))
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}

	data := map[string]interface{}{
		"Package": "resources",
		"Name":    resourceName,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	cli.Success(fmt.Sprintf("Created: %s", outputPath))
	return nil
}

func toResourceName(name string) string {
	name = strings.TrimSuffix(name, "Resource")
	name = strings.TrimSuffix(name, "resource")
	return toPascalCase(name)
}
