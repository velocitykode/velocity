package console

import (
	"strings"

	"github.com/velocitykode/velocity/console/scaffold"
)

// MakeNotificationOptions holds flags for the make:notification command.
type MakeNotificationOptions struct {
	Dir string // --dir output directory override (default internal/notifications)
}

// MakeNotification generates a new notification file from a stub template.
func MakeNotification(name string, opts MakeNotificationOptions) error {
	if err := scaffold.ValidateName(name); err != nil {
		return err
	}

	notificationName := toNotificationName(name)

	data := map[string]interface{}{
		"Package": "notifications",
		"Name":    notificationName,
	}

	return writeScaffoldedFile(name, opts.Dir, "internal/notifications", "notification", toSnakeCase(notificationName)+".go", "internal/notifications/notification.go.stub", nil, data)
}

func toNotificationName(name string) string {
	name = strings.TrimSuffix(name, "Notification")
	name = strings.TrimSuffix(name, "notification")
	return toPascalCase(name)
}
