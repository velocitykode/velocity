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

// MakeJobOptions holds flags for the make:job command.
type MakeJobOptions struct{}

// MakeJob generates a new queue job file from a stub template.
func MakeJob(name string, opts MakeJobOptions) error {
	if err := validateMakeName(name); err != nil {
		return err
	}

	jobName := toJobName(name)

	outputDir := "internal/jobs"
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	filename := toSnakeCase(jobName) + ".go"
	outputPath := filepath.Join(outputDir, filename)
	if err := ensureWithinRoot(outputDir, outputPath); err != nil {
		return fmt.Errorf("invalid job name %q: %w", name, err)
	}

	if _, err := os.Stat(outputPath); err == nil {
		return fmt.Errorf("job already exists: %s", outputPath)
	}

	stubContent, err := stubs.Get("internal/jobs/job.go.stub")
	if err != nil {
		return fmt.Errorf("failed to read stub: %w", err)
	}

	tmpl, err := template.New("job").Parse(string(stubContent))
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}

	data := map[string]interface{}{
		"Package": "jobs",
		"Name":    jobName,
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

func toJobName(name string) string {
	name = strings.TrimSuffix(name, "Job")
	name = strings.TrimSuffix(name, "job")
	return toPascalCase(name)
}
