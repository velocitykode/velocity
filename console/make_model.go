package console

import (
	"fmt"
	"strings"

	"github.com/velocitykode/velocity/console/scaffold"
	"github.com/velocitykode/velocity/str"
)

// MakeModelOptions holds flags for the make:model command.
type MakeModelOptions struct {
	UUID        bool
	SoftDeletes bool
	Migration   bool
	Dir         string // --dir output directory override (default internal/models)
}

// MakeModel generates a new model file from a stub template.
func MakeModel(name string, opts MakeModelOptions) error {
	if err := scaffold.ValidateName(name); err != nil {
		return err
	}

	modelName := toModelName(name)
	tableName := toTableName(modelName)

	data := map[string]interface{}{
		"ModelName":   modelName,
		"TableName":   tableName,
		"UUID":        opts.UUID,
		"SoftDeletes": opts.SoftDeletes,
	}

	if err := writeScaffoldedFile(name, opts.Dir, "internal/models", "model", toSnakeCase(modelName)+".go", "internal/models/model.go.stub", nil, data); err != nil {
		return err
	}

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
