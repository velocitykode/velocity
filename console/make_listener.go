package console

import (
	"strings"

	"github.com/velocitykode/velocity/console/scaffold"
)

// MakeListenerOptions holds flags for the make:listener command.
type MakeListenerOptions struct {
	Dir string // --dir output directory override (default internal/listeners)
}

// MakeListener generates a new listener file from a stub template.
func MakeListener(name string, opts MakeListenerOptions) error {
	if err := scaffold.ValidateName(name); err != nil {
		return err
	}

	listenerName := toListenerName(name)

	fallback := []byte(`package {{ .Package }}

// {{ .Name }} handles an event
func {{ .Name }}(event interface{}) error {
	// Handle the event
	return nil
}
`)

	data := map[string]interface{}{
		"Package": "listeners",
		"Name":    listenerName,
	}

	return writeScaffoldedFile(name, opts.Dir, "internal/listeners", "listener", toSnakeCase(listenerName)+".go", "internal/listeners/listener.go.stub", fallback, data)
}

func toListenerName(name string) string {
	name = strings.TrimSuffix(name, "Listener")
	name = strings.TrimSuffix(name, "listener")
	return toPascalCase(name)
}
