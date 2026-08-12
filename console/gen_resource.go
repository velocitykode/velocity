package console

import (
	"strings"

	"github.com/velocitykode/velocity/console/scaffold"
)

// GenResourceOptions holds flags for the gen resource command.
type GenResourceOptions struct {
	Dir string // --dir output directory override (default internal/resources)
}

// GenResource generates a new resource file from a stub template.
func GenResource(name string, opts GenResourceOptions) error {
	if err := scaffold.ValidateName(name); err != nil {
		return err
	}

	resourceName := toResourceName(name)
	if err := requireNormalizedName(name, resourceName, "resource"); err != nil {
		return err
	}

	data := map[string]interface{}{
		"Package": "resources",
		"Name":    resourceName,
	}

	return writeScaffoldedFile(name, opts.Dir, "internal/resources", "resource", toSnakeCase(resourceName)+".go", "internal/resources/resource.go.stub", data)
}

func toResourceName(name string) string {
	name = strings.TrimSuffix(name, "Resource")
	name = strings.TrimSuffix(name, "resource")
	return toPascalCase(name)
}
