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

// MakePolicyOptions holds flags for the make:policy command.
type MakePolicyOptions struct {
	Dir string // --dir output directory override (default internal/policies)
}

// MakePolicy generates a new policy file from a stub template.
func MakePolicy(name string, opts MakePolicyOptions) error {
	if err := validateMakeName(name); err != nil {
		return err
	}

	policyName := toPolicyName(name)

	outputDir, err := resolveMakeDir("internal/policies", opts.Dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(outputDir, defaultDirMode); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	filename := toSnakeCase(policyName) + ".go"
	outputPath := filepath.Join(outputDir, filename)
	if err := ensureWithinRoot(outputDir, outputPath); err != nil {
		return fmt.Errorf("invalid policy name %q: %w", name, err)
	}

	if err := ensureWritableTarget(outputPath, "policy"); err != nil {
		return err
	}

	stubContent, err := stubs.Get("internal/policies/policy.go.stub")
	if err != nil {
		stubContent = []byte(`package {{ .Package }}

import "net/http"

// {{ .Name }}Policy defines authorization rules for the {{ .Name }} resource.
type {{ .Name }}Policy struct{}

// View determines if the user can view the resource.
func (p {{ .Name }}Policy) View(r *http.Request, model any) bool {
	return true
}

// Create determines if the user can create a resource.
func (p {{ .Name }}Policy) Create(r *http.Request) bool {
	return true
}

// Update determines if the user can update the resource.
func (p {{ .Name }}Policy) Update(r *http.Request, model any) bool {
	return true
}

// Delete determines if the user can delete the resource.
func (p {{ .Name }}Policy) Delete(r *http.Request, model any) bool {
	return true
}
`)
	}

	tmpl, err := template.New("policy").Parse(string(stubContent))
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}

	data := map[string]interface{}{
		"Package": "policies",
		"Name":    policyName,
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

func toPolicyName(name string) string {
	name = strings.TrimSuffix(name, "Policy")
	name = strings.TrimSuffix(name, "policy")
	return toPascalCase(name)
}
