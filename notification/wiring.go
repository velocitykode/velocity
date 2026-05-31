package notification

import (
	"database/sql"

	"github.com/velocitykode/velocity/mail"
)

// MailerAware marks channels that can be wired with the framework mailer.
type MailerAware interface {
	SetMailer(mail.Mailer)
}

// DBAware marks channels that can be wired with the framework database handle.
type DBAware interface {
	SetDB(db *sql.DB, driver ...string)
}
