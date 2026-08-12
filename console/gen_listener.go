package console

import (
	"strings"

	"github.com/velocitykode/velocity/console/scaffold"
)

// GenListenerOptions holds flags for the gen listener command.
type GenListenerOptions struct {
	Dir string // --dir output directory override (default internal/listeners)
}

// GenListener generates a new listener file from a stub template.
func GenListener(name string, opts GenListenerOptions) error {
	if err := scaffold.ValidateName(name); err != nil {
		return err
	}

	listenerName := toListenerName(name)

	data := map[string]interface{}{
		"Package": "listeners",
		"Name":    listenerName,
	}

	return writeScaffoldedFile(name, opts.Dir, "internal/listeners", "listener", toSnakeCase(listenerName)+".go", "internal/listeners/listener.go.stub", data)
}

func toListenerName(name string) string {
	name = strings.TrimSuffix(name, "Listener")
	name = strings.TrimSuffix(name, "listener")
	return toPascalCase(name)
}
