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

// MakeHandlerOptions holds flags for the make:handler command.
type MakeHandlerOptions struct {
	Resource bool
	API      bool
}

// MakeHandler generates a new handler file from a stub template.
func MakeHandler(name string, opts MakeHandlerOptions) error {
	handlerName := toHandlerName(name)

	packageName := "handlers"
	outputDir := "internal/handlers"

	if strings.Contains(name, "/") {
		parts := strings.Split(name, "/")
		handlerName = toHandlerName(parts[len(parts)-1])
		packageName = strings.ToLower(parts[len(parts)-2])
		for i := range parts[:len(parts)-1] {
			parts[i] = strings.ToLower(parts[i])
		}
		outputDir = filepath.Join("internal/handlers", filepath.Join(parts[:len(parts)-1]...))
	}

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	filename := toSnakeCase(handlerName) + ".go"
	outputPath := filepath.Join(outputDir, filename)

	if _, err := os.Stat(outputPath); err == nil {
		return fmt.Errorf("handler already exists: %s", outputPath)
	}

	stubContent, err := stubs.Get("internal/handlers/handler.go.stub")
	if err != nil {
		stubContent = []byte(`package {{ .Package }}

import "github.com/velocitykode/velocity/router"

func {{ .HandlerName }}(ctx *router.Context) error {
	return ctx.String(200, "{{ .HandlerName }}")
}
`)
	}

	tmpl, err := template.New("handler").Parse(string(stubContent))
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}

	data := map[string]interface{}{
		"Package":     packageName,
		"HandlerName": handlerName,
		"Resource":    opts.Resource,
		"API":         opts.API,
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

func toHandlerName(name string) string {
	name = strings.TrimSuffix(name, "Handler")
	name = strings.TrimSuffix(name, "handler")
	name = strings.TrimSuffix(name, "Controller")
	name = strings.TrimSuffix(name, "controller")
	return toPascalCase(name)
}

func toPascalCase(s string) string {
	words := splitWords(s)
	for i, word := range words {
		words[i] = strings.Title(strings.ToLower(word))
	}
	return strings.Join(words, "")
}

func toSnakeCase(s string) string {
	var result []rune
	for i, r := range s {
		if unicode.IsUpper(r) && i > 0 {
			result = append(result, '_')
		}
		result = append(result, unicode.ToLower(r))
	}
	return string(result)
}

func splitWords(s string) []string {
	var words []string
	var current []rune

	for _, r := range s {
		if r == '_' || r == '-' || r == ' ' {
			if len(current) > 0 {
				words = append(words, string(current))
				current = nil
			}
		} else if unicode.IsUpper(r) && len(current) > 0 {
			words = append(words, string(current))
			current = []rune{r}
		} else {
			current = append(current, r)
		}
	}

	if len(current) > 0 {
		words = append(words, string(current))
	}

	return words
}
