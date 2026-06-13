package console

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	cli "github.com/velocitykode/velocity-cli"
	"github.com/velocitykode/velocity/console/stubs"
)

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
	if err := validateMakeName(name); err != nil {
		return err
	}
	if err := validateTableName("--create", opts.Create); err != nil {
		return err
	}
	if err := validateTableName("--table", opts.Table); err != nil {
		return err
	}

	version := time.Now().Format("20060102150405")
	snakeName := toSnakeCase(toPascalCase(name))

	outputDir, err := resolveMakeDir("database/migrations", opts.Dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(outputDir, defaultDirMode); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	filename := version + "_" + snakeName + ".go"
	outputPath := filepath.Join(outputDir, filename)
	if err := ensureWithinRoot(outputDir, outputPath); err != nil {
		return fmt.Errorf("invalid migration name %q: %w", name, err)
	}

	if err := ensureWritableTarget(outputPath, "migration"); err != nil {
		return err
	}

	stubContent, err := stubs.Get("database/migrations/migration.go.stub")
	if err != nil {
		return fmt.Errorf("failed to read stub: %w", err)
	}

	tmpl, err := template.New("migration").Parse(string(stubContent))
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}

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

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	if err := os.WriteFile(outputPath, buf.Bytes(), defaultFileMode); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	cli.Success(fmt.Sprintf("Created: %s", outputPath))
	return nil
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
