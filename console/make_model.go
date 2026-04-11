package console

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	cli "github.com/velocitykode/velocity-cli"
	"github.com/velocitykode/velocity/console/stubs"
	"github.com/velocitykode/velocity/str"
)

// MakeModelOptions holds flags for the make:model command.
type MakeModelOptions struct {
	UUID        bool
	SoftDeletes bool
	Migration   bool
}

// MakeModel generates a new model file from a stub template.
func MakeModel(name string, opts MakeModelOptions) error {
	modelName := toModelName(name)
	tableName := toTableName(modelName)

	outputDir := "internal/models"
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	filename := toSnakeCase(modelName) + ".go"
	outputPath := filepath.Join(outputDir, filename)

	if _, err := os.Stat(outputPath); err == nil {
		return fmt.Errorf("model already exists: %s", outputPath)
	}

	stubContent, err := stubs.Get("internal/models/model.go.stub")
	if err != nil {
		return fmt.Errorf("failed to read stub: %w", err)
	}

	tmpl, err := template.New("model").Parse(string(stubContent))
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}

	data := map[string]interface{}{
		"ModelName":   modelName,
		"TableName":   tableName,
		"UUID":        opts.UUID,
		"SoftDeletes": opts.SoftDeletes,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	cli.Success(fmt.Sprintf("Created: %s", outputPath))

	if opts.Migration {
		migrationName := "create_" + tableName
		migrationOpts := MakeMigrationOptions{
			Create:      tableName,
			UUID:        opts.UUID,
			SoftDeletes: opts.SoftDeletes,
		}
		if err := MakeMigration(migrationName, migrationOpts); err != nil {
			return fmt.Errorf("failed to create migration: %w", err)
		}
	}

	return nil
}

func toModelName(name string) string {
	name = strings.TrimSuffix(name, "Model")
	name = strings.TrimSuffix(name, "model")
	return toPascalCase(name)
}

func toTableName(modelName string) string {
	snake := toSnakeCase(modelName)
	return str.Plural(snake)
}
