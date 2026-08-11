package console

import (
	"strings"

	"github.com/velocitykode/velocity/console/scaffold"
)

// MakeModuleOptions holds flags for the gen module command.
type MakeModuleOptions struct {
	Dir string // --dir output directory override (default internal/providers)
}

// MakeModule generates a new module file from a stub template.
func MakeModule(name string, opts MakeModuleOptions) error {
	if err := scaffold.ValidateName(name); err != nil {
		return err
	}

	moduleName := toModuleName(name)

	data := map[string]interface{}{
		"Package": "providers",
		"Name":    moduleName,
	}

	return writeScaffoldedFile(name, opts.Dir, "internal/providers", "module", toSnakeCase(moduleName)+".go", "internal/providers/provider.go.stub", data)
}

// toModuleName strips a trailing Module/Provider/ServiceProvider suffix from
// the user-supplied name (the stub appends "Module" itself) and returns it in
// PascalCase.
func toModuleName(name string) string {
	name = strings.TrimSuffix(name, "ServiceProvider")
	name = strings.TrimSuffix(name, "Provider")
	name = strings.TrimSuffix(name, "provider")
	name = strings.TrimSuffix(name, "Module")
	name = strings.TrimSuffix(name, "module")
	return toPascalCase(name)
}
