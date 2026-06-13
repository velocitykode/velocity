package console

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/velocitykode/velocity/console/scaffold"
)

// timeNow is the clock source for migration versions. It is a package
// variable so tests can pin it to a fixed instant and deterministically
// exercise the same-second collision guard.
var timeNow = time.Now

// MakeMigrationOptions holds flags for the make:migration command.
type MakeMigrationOptions struct {
	Create      string // table name for create-table boilerplate
	Table       string // table name for alter-table boilerplate
	UUID        bool   // use UUIDPrimary instead of ID
	SoftDeletes bool   // include SoftDeletes column
	Dir         string // --dir output directory override (default database/migrations)
}

// MakeMigration generates a new timestamped migration file from a stub template.
func MakeMigration(name string, opts MakeMigrationOptions) error {
	if err := scaffold.ValidateName(name); err != nil {
		return err
	}
	if err := validateTableName("--create", opts.Create); err != nil {
		return err
	}
	if err := validateTableName("--table", opts.Table); err != nil {
		return err
	}

	version := timeNow().Format("20060102150405")
	snakeName := toSnakeCase(toPascalCase(name))

	// The version is second-resolution, so two migrations scaffolded in the
	// same second would share a version and make migrate.Register panic with
	// "duplicate migration version" at app boot. Probe the target directory
	// (resolved the same way the scaffold leaf will resolve it, honoring
	// opts.Dir) and bump the version forward a second at a time until no file
	// carries that version prefix. Capped so a pathological directory can't
	// spin forever.
	outputDir, err := scaffold.ResolveDir("database/migrations", opts.Dir)
	if err != nil {
		return err
	}
	for attempts := 0; ; attempts++ {
		matches, err := filepath.Glob(filepath.Join(outputDir, version+"*"))
		if err != nil {
			return err
		}
		if len(matches) == 0 {
			break
		}
		if attempts >= 60 {
			return fmt.Errorf("could not allocate a unique migration version in %s after 60 attempts", outputDir)
		}
		t, err := time.Parse("20060102150405", version)
		if err != nil {
			return err
		}
		version = t.Add(time.Second).Format("20060102150405")
	}

	filename := version + "_" + snakeName + ".go"
	description := toDescription(snakeName)

	data := map[string]interface{}{
		"Version":     version,
		"Description": description,
		"TableName":   opts.Create,
		"CreateTable": opts.Create != "",
		"AlterTable":  opts.Table != "",
		"UUID":        opts.UUID,
		"SoftDeletes": opts.SoftDeletes,
	}

	// If alter table, use that table name
	if opts.Table != "" {
		data["TableName"] = opts.Table
	}

	return writeScaffoldedFile(name, opts.Dir, "database/migrations", "migration", filename, "database/migrations/migration.go.stub", data)
}

// validateTableName ensures a --create/--table flag value is a plain SQL
// identifier: [A-Za-z_][A-Za-z0-9_]*. The value lands verbatim in the
// generated migration's TableName via text/template with no escaping, so
// quotes, backticks, semicolons, or newlines would otherwise be written
// straight into Go source. Empty means the flag was not passed.
func validateTableName(flag, table string) error {
	if table == "" {
		return nil
	}
	for i, r := range table {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_':
			// letters and '_' allowed anywhere
		case i > 0 && r >= '0' && r <= '9':
			// digits allowed after the first character
		default:
			if i == 0 {
				return fmt.Errorf("invalid %s value %q: must start with a letter or underscore, found %q", flag, table, r)
			}
			return fmt.Errorf("invalid %s value %q: character %q not allowed (table names must match [A-Za-z_][A-Za-z0-9_]*)", flag, table, r)
		}
	}
	return nil
}

// toDescription converts a snake_case name to a human-readable description.
// e.g. "create_users" -> "Create users"
func toDescription(name string) string {
	words := strings.Split(name, "_")
	if len(words) > 0 && len(words[0]) > 0 {
		words[0] = strings.ToUpper(words[0][:1]) + words[0][1:]
	}
	return strings.Join(words, " ")
}
