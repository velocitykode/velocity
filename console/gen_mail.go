package console

import (
	"strings"

	"github.com/velocitykode/velocity/console/scaffold"
	"github.com/velocitykode/velocity/str"
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
	if err := requireNormalizedName(name, mailName, "mailable"); err != nil {
		return err
	}

	data := map[string]interface{}{
		"Package": "mail",
		"Name":    mailName,
	}

	return writeScaffoldedFile(name, opts.Dir, "internal/mail", "mailable", toSnakeCase(mailName)+".go", "internal/mail/mailable.go.stub", data)
}

// toMailName strips the redundant kind suffix from a user-supplied mailable
// name.
//
// "Mailable" / "mailable" / "Mail" come off with a plain TrimSuffix, matching
// every other generator in this family. The lowercase "mail" cannot: unlike
// "model", "job", or "policy", it is a common ending of a real mailable name,
// so trimming it blindly would turn the canonical "WelcomeEmail" into
// "WelcomeE". It is stripped only where it stands as its own word, which
// str.Snake decides - it splits on case changes and on "_" / "-", so
// "WelcomeEmail" -> "welcome_email" (kept) while "welcome-mail" and a bare
// "mail" -> "welcome_mail" / "mail" (stripped, the latter leaving the empty
// name requireNormalizedName rejects).
func toMailName(name string) string {
	name = strings.TrimSuffix(name, "Mailable")
	name = strings.TrimSuffix(name, "mailable")
	name = strings.TrimSuffix(name, "Mail")
	if snake := str.Snake(name); snake == "mail" || strings.HasSuffix(snake, "_mail") {
		name = strings.TrimSuffix(name, "mail")
	}
	return toPascalCase(name)
}
