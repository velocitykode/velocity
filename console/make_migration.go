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
}

// MakeMigration generates a new timestamped migration file from a stub template.
func MakeMigration(name string, opts MakeMigrationOptions) error {
	version := time.Now().Format("20060102150405")
	snakeName := toSnakeCase(toPascalCase(name))

	outputDir := "database/migrations"
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	filename := version + "_" + snakeName + ".go"
	outputPath := filepath.Join(outputDir, filename)

	if _, err := os.Stat(outputPath); err == nil {
		return fmt.Errorf("migration already exists: %s", outputPath)
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

	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	cli.Success(fmt.Sprintf("Created: %s", outputPath))
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
