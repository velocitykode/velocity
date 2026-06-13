package console

import (
	"strings"

	"github.com/velocitykode/velocity/console/scaffold"
)

// MakeMiddlewareOptions holds flags for the make:middleware command.
type MakeMiddlewareOptions struct {
	Dir string // --dir output directory override (default internal/middleware)
}

// MakeMiddleware generates a new middleware file from a stub template.
func MakeMiddleware(name string, opts MakeMiddlewareOptions) error {
	if err := scaffold.ValidateName(name); err != nil {
		return err
	}

	middlewareName := toMiddlewareName(name)

	data := map[string]interface{}{
		"Package": "middleware",
		"Name":    middlewareName,
	}

	return writeScaffoldedFile(name, opts.Dir, "internal/middleware", "middleware", toSnakeCase(middlewareName)+".go", "internal/middleware/generated.go.stub", data)
}

func toMiddlewareName(name string) string {
	name = strings.TrimSuffix(name, "Middleware")
	name = strings.TrimSuffix(name, "middleware")
	return toPascalCase(name)
}
