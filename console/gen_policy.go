package console

import (
	"strings"

	"github.com/velocitykode/velocity/console/scaffold"
)

// GenPolicyOptions holds flags for the gen policy command.
type GenPolicyOptions struct {
	Dir string // --dir output directory override (default internal/policies)
}

// GenPolicy generates a new policy file from a stub template.
func GenPolicy(name string, opts GenPolicyOptions) error {
	if err := scaffold.ValidateName(name); err != nil {
		return err
	}

	policyName := toPolicyName(name)
	if err := requireNormalizedName(name, policyName, "policy"); err != nil {
		return err
	}

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
