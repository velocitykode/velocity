package validation

import (
	"github.com/velocitykode/velocity/contract"
)

// ErrValidationFailed is the sentinel returned (wrapped) whenever a
// validation.Result reports one or more field errors. Callers can use
// errors.Is(err, validation.ErrValidationFailed) to branch on the generic
// "validation failed" condition without inspecting per-field messages.
// Canonical identity lives in the stdlib-only contract leaf.
var ErrValidationFailed = contract.ErrValidationFailed

// ValidationErrors represents validation errors. Canonical declaration
// (struct and methods) lives in the contract leaf; this alias keeps the
// validation API byte-identical for existing callers.
type ValidationErrors = contract.ValidationErrors
