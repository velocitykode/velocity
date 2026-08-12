package console

import (
	"strings"

	"github.com/velocitykode/velocity/console/scaffold"
)

// GenMailOptions holds flags for the gen mail command.
type GenMailOptions struct {
	Dir string // --dir output directory override (default internal/mail)
}

// GenMail generates a new mailable file from a stub template.
func GenMail(name string, opts GenMailOptions) error {
	if err := scaffold.ValidateName(name); err != nil {
		return err
	}

	mailName := toMailName(name)

	data := map[string]interface{}{
		"Package": "mail",
		"Name":    mailName,
	}

	return writeScaffoldedFile(name, opts.Dir, "internal/mail", "mailable", toSnakeCase(mailName)+".go", "internal/mail/mailable.go.stub", data)
}

func toMailName(name string) string {
	name = strings.TrimSuffix(name, "Mailable")
	name = strings.TrimSuffix(name, "mailable")
	name = strings.TrimSuffix(name, "Mail")
	return toPascalCase(name)
}
