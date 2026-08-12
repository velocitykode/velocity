package console

import (
	"fmt"
	"strings"

	"github.com/velocitykode/velocity/console/scaffold"
	"github.com/velocitykode/velocity/str"
)

// GenModelOptions holds flags for the gen model command.
type GenModelOptions struct {
	UUID        bool
	SoftDeletes bool
	Migration   bool
	Dir         string // --dir output directory override (default internal/models)
}

// GenModel generates a new model file from a stub template.
func GenModel(name string, opts GenModelOptions) error {
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

	if err := writeScaffoldedFile(name, opts.Dir, "internal/models", "model", toSnakeCase(modelName)+".go", "internal/models/model.go.stub", data); err != nil {
		return err
	}

	if opts.Migration {
		migrationName := "create_" + tableName
		migrationOpts := GenMigrationOptions{
			Create:      tableName,
			UUID:        opts.UUID,
			SoftDeletes: opts.SoftDeletes,
		}
		if err := GenMigration(migrationName, migrationOpts); err != nil {
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
