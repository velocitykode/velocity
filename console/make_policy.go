package console

import (
	"strings"

	"github.com/velocitykode/velocity/console/scaffold"
)

// MakePolicyOptions holds flags for the make:policy command.
type MakePolicyOptions struct {
	Dir string // --dir output directory override (default internal/policies)
}

// MakePolicy generates a new policy file from a stub template.
func MakePolicy(name string, opts MakePolicyOptions) error {
	if err := scaffold.ValidateName(name); err != nil {
		return err
	}

	policyName := toPolicyName(name)

	fallback := []byte(`package {{ .Package }}

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

	data := map[string]interface{}{
		"Package": "policies",
		"Name":    policyName,
	}

	return writeScaffoldedFile(name, opts.Dir, "internal/policies", "policy", toSnakeCase(policyName)+".go", "internal/policies/policy.go.stub", fallback, data)
}

func toPolicyName(name string) string {
	name = strings.TrimSuffix(name, "Policy")
	name = strings.TrimSuffix(name, "policy")
	return toPascalCase(name)
}
