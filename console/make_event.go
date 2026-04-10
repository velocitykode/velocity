package console

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"unicode"

	"github.com/velocitykode/velocity/console/stubs"
)

// MakeEventOptions holds flags for the make:event command.
type MakeEventOptions struct{}

// MakeEvent generates a new event file from a stub template.
func MakeEvent(name string, opts MakeEventOptions) error {
	eventName := toEventName(name)
	dotName := toDotSeparated(eventName)

	outputDir := "internal/events"
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	filename := toSnakeCase(eventName) + ".go"
	outputPath := filepath.Join(outputDir, filename)

	if _, err := os.Stat(outputPath); err == nil {
		return fmt.Errorf("event already exists: %s", outputPath)
	}

	stubContent, err := stubs.Get("internal/events/event.go.stub")
	if err != nil {
		stubContent = []byte(`package {{ .Package }}

// {{ .Name }} event
type {{ .Name }} struct {
	// Add event data fields here
}

// Name returns the event name
func (e {{ .Name }}) Name() string {
	return "{{ .EventName }}"
}
`)
	}

	tmpl, err := template.New("event").Parse(string(stubContent))
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}

	data := map[string]interface{}{
		"Package":   "events",
		"Name":      eventName,
		"EventName": dotName,
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
