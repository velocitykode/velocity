package console

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"unicode"

	cli "github.com/velocitykode/velocity-cli"
	"github.com/velocitykode/velocity/console/stubs"
)

// MakeCommandOptions holds flags for the make:command command.
type MakeCommandOptions struct{}

// MakeCommand generates a new command file from a stub template.
func MakeCommand(name string, opts MakeCommandOptions) error {
	commandName := toCommandStructName(name)
	kebabName := toKebabCase(commandName)

	outputDir := "internal/commands"
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	filename := toSnakeCase(commandName) + ".go"
	outputPath := filepath.Join(outputDir, filename)

	if _, err := os.Stat(outputPath); err == nil {
		return fmt.Errorf("command already exists: %s", outputPath)
	}

	stubContent, err := stubs.Get("internal/commands/command.go.stub")
	if err != nil {
		stubContent = []byte(`package {{ .Package }}

import (
	"fmt"

	"github.com/velocitykode/velocity/app"
)

// {{ .Name }}Command is a console command.
//
// Register this command in internal/commands/kernel.go:
//   r.Add(&{{ .Name }}Command{})
type {{ .Name }}Command struct{}

// Name returns the command name used to invoke it.
func (c {{ .Name }}Command) Name() string {
	return "{{ .CommandName }}"
}

// Description returns a short description of the command.
func (c {{ .Name }}Command) Description() string {
	return "{{ .CommandName }} command (update this description)"
}

// Handle executes the command logic.
func (c {{ .Name }}Command) Handle(s *app.Services, args []string) error {
	fmt.Println("Executing {{ .CommandName }}...")
	return nil
}
`)
	}

	tmpl, err := template.New("command").Parse(string(stubContent))
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}

	data := map[string]interface{}{
		"Package":     "commands",
		"Name":        commandName,
		"CommandName": kebabName,
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

func toCommandStructName(name string) string {
	name = strings.TrimSuffix(name, "Command")
	name = strings.TrimSuffix(name, "command")
	return toPascalCase(name)
}

// toKebabCase converts PascalCase to kebab-case.
func toKebabCase(s string) string {
	var result []rune
	for i, r := range s {
		if unicode.IsUpper(r) && i > 0 {
			result = append(result, '-')
		}
		result = append(result, unicode.ToLower(r))
	}
	return string(result)
}
