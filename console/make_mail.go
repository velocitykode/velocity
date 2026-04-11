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

// MakeMailOptions holds flags for the make:mail command.
type MakeMailOptions struct{}

// MakeMail generates a new mailable file from a stub template.
func MakeMail(name string, opts MakeMailOptions) error {
	mailName := toMailName(name)

	outputDir := "internal/mail"
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	filename := toSnakeCase(mailName) + ".go"
	outputPath := filepath.Join(outputDir, filename)

	if _, err := os.Stat(outputPath); err == nil {
		return fmt.Errorf("mailable already exists: %s", outputPath)
	}

	stubContent, err := stubs.Get("internal/mail/mailable.go.stub")
	if err != nil {
		return fmt.Errorf("failed to read stub: %w", err)
	}

	tmpl, err := template.New("mailable").Parse(string(stubContent))
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}

	data := map[string]interface{}{
		"Package": "mail",
		"Name":    mailName,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	cli.Success(fmt.Sprintf("Created: %s", outputPath))
	return nil
}

func toMailName(name string) string {
	name = strings.TrimSuffix(name, "Mailable")
	name = strings.TrimSuffix(name, "mailable")
	name = strings.TrimSuffix(name, "Mail")
	return toPascalCase(name)
}
