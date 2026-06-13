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
)

// MakeListenerOptions holds flags for the make:listener command.
type MakeListenerOptions struct {
	Dir string // --dir output directory override (default internal/listeners)
}

// MakeListener generates a new listener file from a stub template.
func MakeListener(name string, opts MakeListenerOptions) error {
	if err := validateMakeName(name); err != nil {
		return err
	}

	listenerName := toListenerName(name)

	outputDir, err := resolveMakeDir("internal/listeners", opts.Dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(outputDir, defaultDirMode); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	filename := toSnakeCase(listenerName) + ".go"
	outputPath := filepath.Join(outputDir, filename)
	if err := ensureWithinRoot(outputDir, outputPath); err != nil {
		return fmt.Errorf("invalid listener name %q: %w", name, err)
	}

	if err := ensureWritableTarget(outputPath, "listener"); err != nil {
		return err
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

	if err := os.WriteFile(outputPath, buf.Bytes(), defaultFileMode); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	cli.Success(fmt.Sprintf("Created: %s", outputPath))
	return nil
}

func toListenerName(name string) string {
	name = strings.TrimSuffix(name, "Listener")
	name = strings.TrimSuffix(name, "listener")
	return toPascalCase(name)
}
