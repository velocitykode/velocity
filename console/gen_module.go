package console

import (
	"strings"

	"github.com/velocitykode/velocity/console/scaffold"
)

// GenModuleOptions holds flags for the gen module command.
type GenModuleOptions struct {
	Dir string // --dir output directory override (default internal/modules)
}

// GenModule generates a new module file from a stub template.
func GenModule(name string, opts GenModuleOptions) error {
	if err := scaffold.ValidateName(name); err != nil {
		return err
	}

	moduleName := toModuleName(name)
	if err := requireNormalizedName(name, moduleName, "module"); err != nil {
		return err
	}

	data := map[string]interface{}{
		"Package": "modules",
		"Name":    moduleName,
	}

	return writeScaffoldedFile(name, opts.Dir, "internal/modules", "module", toSnakeCase(moduleName)+".go", "internal/modules/module.go.stub", data)
}

// toModuleName strips a trailing Module suffix from the user-supplied name
// (the stub appends "Module" itself) and returns it in PascalCase.
func toModuleName(name string) string {
	name = strings.TrimSuffix(name, "Module")
	name = strings.TrimSuffix(name, "module")
	return toPascalCase(name)
}
