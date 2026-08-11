package console

import (
	"strings"

	"github.com/velocitykode/velocity/console/scaffold"
)

// MakePolicyOptions holds flags for the gen policy command.
type MakePolicyOptions struct {
	Dir string // --dir output directory override (default internal/policies)
}

// MakePolicy generates a new policy file from a stub template.
func MakePolicy(name string, opts MakePolicyOptions) error {
	if err := scaffold.ValidateName(name); err != nil {
		return err
	}

	policyName := toPolicyName(name)

	data := map[string]interface{}{
		"Package": "policies",
		"Name":    policyName,
	}

	return writeScaffoldedFile(name, opts.Dir, "internal/policies", "policy", toSnakeCase(policyName)+".go", "internal/policies/policy.go.stub", data)
}

func toPolicyName(name string) string {
	name = strings.TrimSuffix(name, "Policy")
	name = strings.TrimSuffix(name, "policy")
	return toPascalCase(name)
}
