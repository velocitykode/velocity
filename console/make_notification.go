package console

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/velocitykode/velocity/console/stubs"
)

// MakeNotificationOptions holds flags for the make:notification command.
type MakeNotificationOptions struct{}

// MakeNotification generates a new notification file from a stub template.
func MakeNotification(name string, opts MakeNotificationOptions) error {
	notificationName := toNotificationName(name)

	outputDir := "internal/notifications"
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	filename := toSnakeCase(notificationName) + ".go"
	outputPath := filepath.Join(outputDir, filename)

	if _, err := os.Stat(outputPath); err == nil {
		return fmt.Errorf("notification already exists: %s", outputPath)
	}

	stubContent, err := stubs.Get("internal/notifications/notification.go.stub")
	if err != nil {
		return fmt.Errorf("failed to read stub: %w", err)
	}

	tmpl, err := template.New("notification").Parse(string(stubContent))
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}

	data := map[string]interface{}{
		"Package": "notifications",
		"Name":    notificationName,
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

func toNotificationName(name string) string {
	name = strings.TrimSuffix(name, "Notification")
	name = strings.TrimSuffix(name, "notification")
	return toPascalCase(name)
}
