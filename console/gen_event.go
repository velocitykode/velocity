package console

import (
	"strings"
	"unicode"

	"github.com/velocitykode/velocity/console/scaffold"
)

// GenEventOptions holds flags for the gen event command.
type GenEventOptions struct {
	Dir string // --dir output directory override (default internal/events)
}

// GenEvent generates a new event file from a stub template.
func GenEvent(name string, opts GenEventOptions) error {
	if err := scaffold.ValidateName(name); err != nil {
		return err
	}

	eventName := toEventName(name)
	dotName := toDotSeparated(eventName)

	data := map[string]interface{}{
		"Package":   "events",
		"Name":      eventName,
		"EventName": dotName,
	}

	return writeScaffoldedFile(name, opts.Dir, "internal/events", "event", toSnakeCase(eventName)+".go", "internal/events/event.go.stub", data)
}

func toEventName(name string) string {
	name = strings.TrimSuffix(name, "Event")
	name = strings.TrimSuffix(name, "event")
	return toPascalCase(name)
}

// toDotSeparated converts PascalCase to dot.separated lowercase.
// e.g. "UserRegistered" → "user.registered"
func toDotSeparated(s string) string {
	var parts []string
	var current []rune

	for _, r := range s {
		if unicode.IsUpper(r) && len(current) > 0 {
			parts = append(parts, strings.ToLower(string(current)))
			current = []rune{r}
		} else {
			current = append(current, r)
		}
	}

	if len(current) > 0 {
		parts = append(parts, strings.ToLower(string(current)))
	}

	return strings.Join(parts, ".")
}
