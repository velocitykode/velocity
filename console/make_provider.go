package console

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/velocitykode/velocity/console/stubs"
)

// MakeProviderOptions holds flags for the make:provider command.
type MakeProviderOptions struct{}

// MakeProvider generates a new service provider file from a stub template.
func MakeProvider(name string, opts MakeProviderOptions) error {
	providerName := toProviderName(name)

	outputDir := "internal/providers"
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	filename := toSnakeCase(providerName) + ".go"
	outputPath := filepath.Join(outputDir, filename)

	if _, err := os.Stat(outputPath); err == nil {
		return fmt.Errorf("provider already exists: %s", outputPath)
	}

	stubContent, err := stubs.Get("internal/providers/provider.go.stub")
	if err != nil {
		stubContent = []byte(`package {{ .Package }}

import (
	"context"

	"github.com/velocitykode/velocity/app"
)

// {{ .Name }}ServiceProvider registers and boots the {{ .Name }} service.
type {{ .Name }}ServiceProvider struct{}

// Register binds services into the container.
func (p *{{ .Name }}ServiceProvider) Register(s *app.Services) error {
	return nil
}

// Boot is called after all providers have been registered.
func (p *{{ .Name }}ServiceProvider) Boot(s *app.Services) error {
	return nil
}

// Shutdown gracefully tears down provider resources.
func (p *{{ .Name }}ServiceProvider) Shutdown(ctx context.Context) error {
	return nil
}
`)
	}

	tmpl, err := template.New("provider").Parse(string(stubContent))
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}

	data := map[string]interface{}{
		"Package": "providers",
		"Name":    providerName,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	fmt.Printf("Created: %s\n", outputPath)
	return nil
}

func toProviderName(name string) string {
	name = strings.TrimSuffix(name, "ServiceProvider")
	name = strings.TrimSuffix(name, "Provider")
	name = strings.TrimSuffix(name, "provider")
	return toPascalCase(name)
}
