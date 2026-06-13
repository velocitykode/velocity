package console

import (
	"strings"
	"unicode"

	"github.com/velocitykode/velocity/console/scaffold"
)

// MakeCommandOptions holds flags for the make:command command.
type MakeCommandOptions struct {
	Dir string // --dir output directory override (default internal/commands)
}

// MakeCommand generates a new command file from a stub template.
func MakeCommand(name string, opts MakeCommandOptions) error {
	if err := scaffold.ValidateName(name); err != nil {
		return err
	}

	commandName := toCommandStructName(name)
	kebabName := toKebabCase(commandName)

	fallback := []byte(`package {{ .Package }}

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

	data := map[string]interface{}{
		"Package":     "commands",
		"Name":        commandName,
		"CommandName": kebabName,
	}

	return writeScaffoldedFile(name, opts.Dir, "internal/commands", "command", toSnakeCase(commandName)+".go", "internal/commands/command.go.stub", fallback, data)
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
