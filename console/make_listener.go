package console

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/velocitykode/velocity/console/stubs"
)

// MakeListenerOptions holds flags for the make:listener command.
type MakeListenerOptions struct{}

// MakeListener generates a new listener file from a stub template.
func MakeListener(name string, opts MakeListenerOptions) error {
	listenerName := toListenerName(name)

	outputDir := "internal/listeners"
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	filename := toSnakeCase(listenerName) + ".go"
	outputPath := filepath.Join(outputDir, filename)

	if _, err := os.Stat(outputPath); err == nil {
		return fmt.Errorf("listener already exists: %s", outputPath)
	}

	stubContent, err := stubs.Get("internal/listeners/listener.go.stub")
	if err != nil {
		stubContent = []byte(`package {{ .Package }}

// {{ .Name }} handles an event
func {{ .Name }}(event interface{}) error {
	// Handle the event
	return nil
}
`)
	}

	tmpl, err := template.New("listener").Parse(string(stubContent))
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}

	data := map[string]interface{}{
		"Package": "listeners",
		"Name":    listenerName,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	fmt.Printf("Created: %s\n", outputPath)
	return nil
}

func toListenerName(name string) string {
	name = strings.TrimSuffix(name, "Listener")
	name = strings.TrimSuffix(name, "listener")
	return toPascalCase(name)
}
