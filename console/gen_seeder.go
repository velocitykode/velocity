package console

import (
	"strings"

	"github.com/velocitykode/velocity/console/scaffold"
)

// GenSeederOptions holds flags for the gen seeder command.
type GenSeederOptions struct {
	Dir string // --dir output directory override (default database/seeders)
}

// GenSeeder generates a new database seeder file from a stub template. The
// generated type is <Name>Seeder and its Name() is the kebab-case form used
// by `vel db seed --only`; the file is not registered automatically, the stub
// carries the kernel.go hint.
func GenSeeder(name string, opts GenSeederOptions) error {
	if err := scaffold.ValidateName(name); err != nil {
		return err
	}

	seederName := toSeederStructName(name)
	if err := requireNormalizedName(name, seederName, "seeder"); err != nil {
		return err
	}

	data := map[string]interface{}{
		"Package":    "seeders",
		"Name":       seederName,
		"SeederName": toKebabCase(seederName),
	}

	return writeScaffoldedFile(name, opts.Dir, "database/seeders", "seeder", toSnakeCase(seederName)+".go", "database/seeders/seeder.go.stub", data)
}

// toSeederStructName strips a trailing Seeder suffix from the user-supplied
// name (the stub appends "Seeder" itself) and returns it in PascalCase.
func toSeederStructName(name string) string {
	name = strings.TrimSuffix(name, "Seeder")
	name = strings.TrimSuffix(name, "seeder")
	return toPascalCase(name)
}
