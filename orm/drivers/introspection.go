package drivers

import (
	"fmt"
	"regexp"
)

var schemaIdentifierRegex = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// ValidateSchemaIdentifier accepts a single SQL identifier segment for schema
// metadata APIs. It intentionally rejects dot-qualified names because
// introspection methods scope schema/database separately from table names.
func ValidateSchemaIdentifier(name string) error {
	if !schemaIdentifierRegex.MatchString(name) {
		return fmt.Errorf("invalid schema identifier: %q", name)
	}
	return nil
}
