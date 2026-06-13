package console

import (
	"strings"

	"github.com/velocitykode/velocity/console/scaffold"
)

// MakeProviderOptions holds flags for the make:provider command.
type MakeProviderOptions struct {
	Dir string // --dir output directory override (default internal/providers)
}

// MakeProvider generates a new service provider file from a stub template.
func MakeProvider(name string, opts MakeProviderOptions) error {
	if err := scaffold.ValidateName(name); err != nil {
		return err
	}

	providerName := toProviderName(name)

	fallback := []byte(`package {{ .Package }}

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

	data := map[string]interface{}{
		"Package": "providers",
		"Name":    providerName,
	}

	return writeScaffoldedFile(name, opts.Dir, "internal/providers", "provider", toSnakeCase(providerName)+".go", "internal/providers/provider.go.stub", fallback, data)
}

func toProviderName(name string) string {
	name = strings.TrimSuffix(name, "ServiceProvider")
	name = strings.TrimSuffix(name, "Provider")
	name = strings.TrimSuffix(name, "provider")
	return toPascalCase(name)
}
