package console

import (
	"strings"

	"github.com/velocitykode/velocity/console/scaffold"
)

// MakeResourceOptions holds flags for the make:resource command.
type MakeResourceOptions struct {
	Dir string // --dir output directory override (default internal/resources)
}

// MakeResource generates a new resource file from a stub template.
func MakeResource(name string, opts MakeResourceOptions) error {
	if err := scaffold.ValidateName(name); err != nil {
		return err
	}

	resourceName := toResourceName(name)

	fallback := []byte(`package {{ .Package }}

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

	data := map[string]interface{}{
		"Package": "resources",
		"Name":    resourceName,
	}

	return writeScaffoldedFile(name, opts.Dir, "internal/resources", "resource", toSnakeCase(resourceName)+".go", "internal/resources/resource.go.stub", fallback, data)
}

func toResourceName(name string) string {
	name = strings.TrimSuffix(name, "Resource")
	name = strings.TrimSuffix(name, "resource")
	return toPascalCase(name)
}
