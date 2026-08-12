package console

import (
	"strings"

	"github.com/velocitykode/velocity/console/scaffold"
)

// GenMiddlewareOptions holds flags for the gen middleware command.
type GenMiddlewareOptions struct {
	Dir string // --dir output directory override (default internal/middleware)
}

// GenMiddleware generates a new middleware file from a stub template.
func GenMiddleware(name string, opts GenMiddlewareOptions) error {
	if err := scaffold.ValidateName(name); err != nil {
		return err
	}

	middlewareName := toMiddlewareName(name)
	if err := requireNormalizedName(name, middlewareName, "middleware"); err != nil {
		return err
	}

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
