package mail

import (
	"github.com/velocitykode/velocity/contract"
)

// Mailer is the interface that all mail drivers must implement. The canonical
// declaration lives in the stdlib-only contract leaf; this alias keeps the
// mail API byte-identical for existing callers and drivers.
//
// Implementations must pass mailtest.RunDriverContractTests. See mailtest
// for the executable specification.
type Mailer = contract.Mailer

// Priority levels for emails. Canonical declaration in the contract leaf.
type Priority = contract.Priority

const (
	LowPriority    = contract.LowPriority
	NormalPriority = contract.NormalPriority
	HighPriority   = contract.HighPriority
)

// Address represents an email address. Canonical declaration (with its
// NewAddress constructor, Validate, and String methods) lives in the
// contract leaf.
type Address = contract.Address

// NewAddress creates and validates an email address. Canonical implementation
// in the contract leaf.
var NewAddress = contract.NewAddress

// Attachment represents an email attachment. Canonical declaration in the
// contract leaf.
type Attachment = contract.Attachment
