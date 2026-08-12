package contract

// ValidationRuleSpec is the structured description of one rule instance.
//
// Params are pre-split: the engine consumes them verbatim and never
// re-tokenizes them, so a parameter may contain any byte, including the
// ',' and '|' characters that the historical token grammar could not
// carry (regex alternations, value lists with embedded commas).
//
// Handler is non-nil only for self-carrying custom rules. A carried
// handler is registered on the validator that runs the rule set, so a
// custom rule works on the fresh-validator path without prior
// registration on a long-lived validator.
//
// Params is shared with the rule value that produced the spec and must be
// treated as read-only by every consumer.
type ValidationRuleSpec struct {
	Name    string
	Params  []string
	Handler RuleHandler
}

// ValidationRule describes a single validation rule. Implementations must
// be immutable values: one rule value may be shared by a package-level rule
// set and evaluated concurrently by many goroutines.
type ValidationRule interface {
	Rule() ValidationRuleSpec
}

// ValidationRuleSet maps field names to the rules applied to that field.
//
// It is a defined type, not an alias: interfaces declared against it
// (notably the form-request contract) cannot be satisfied by a
// structurally identical map type, so an adopter method that drifts from
// this type fails to compile instead of silently skipping validation.
type ValidationRuleSet map[string][]ValidationRule

// ValidationMessageKey addresses one field+rule pair for message overrides.
// Rule is the rule name that produced the error, e.g. "required".
type ValidationMessageKey struct {
	Field string
	Rule  string
}

// ValidationMessageSet maps a field+rule pair to the message that replaces
// the built-in one.
type ValidationMessageSet map[ValidationMessageKey]string
