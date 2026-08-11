package console

import (
	"strings"

	"github.com/velocitykode/velocity/console/scaffold"
)

// MakeResourceOptions holds flags for the gen resource command.
type MakeResourceOptions struct {
	Dir string // --dir output directory override (default internal/resources)
}

// MakeResource generates a new resource file from a stub template.
func MakeResource(name string, opts MakeResourceOptions) error {
	if err := scaffold.ValidateName(name); err != nil {
		return err
	}

	resourceName := toResourceName(name)

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
