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

// MakeNotificationOptions holds flags for the make:notification command.
type MakeNotificationOptions struct {
	Dir string // --dir output directory override (default internal/notifications)
}

// MakeNotification generates a new notification file from a stub template.
func MakeNotification(name string, opts MakeNotificationOptions) error {
	if err := validateMakeName(name); err != nil {
		return err
	}

	notificationName := toNotificationName(name)

	outputDir, err := resolveMakeDir("internal/notifications", opts.Dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(outputDir, defaultDirMode); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	filename := toSnakeCase(notificationName) + ".go"
	outputPath := filepath.Join(outputDir, filename)
	if err := ensureWithinRoot(outputDir, outputPath); err != nil {
		return fmt.Errorf("invalid notification name %q: %w", name, err)
	}

	if err := ensureWritableTarget(outputPath, "notification"); err != nil {
		return err
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

	if err := os.WriteFile(outputPath, buf.Bytes(), defaultFileMode); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	cli.Success(fmt.Sprintf("Created: %s", outputPath))
	return nil
}

func toNotificationName(name string) string {
	name = strings.TrimSuffix(name, "Notification")
	name = strings.TrimSuffix(name, "notification")
	return toPascalCase(name)
}
