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

	data := map[string]interface{}{
		"Package": "providers",
		"Name":    providerName,
	}

	return writeScaffoldedFile(name, opts.Dir, "internal/providers", "provider", toSnakeCase(providerName)+".go", "internal/providers/provider.go.stub", data)
}

func toProviderName(name string) string {
	name = strings.TrimSuffix(name, "ServiceProvider")
	name = strings.TrimSuffix(name, "Provider")
	name = strings.TrimSuffix(name, "provider")
	return toPascalCase(name)
}
