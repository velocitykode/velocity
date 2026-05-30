package postgres

import (
	"errors"

	"github.com/lib/pq"

	"github.com/velocitykode/velocity/validation"
)

// init registers a typed UNIQUE-violation classifier with the core validation
// package so validation/dbrules.AsValidationError can attribute a Postgres
// unique-constraint race to the right field via *pq.Error instead of error
// string matching. Importing this leaf (directly or via orm/standard) wires
// it; the core validation package keeps no lib/pq dependency of its own.
func init() {
	validation.RegisterUniqueViolationClassifier(classifyUniqueViolation)
}

// classifyUniqueViolation reports whether err is a *pq.Error and, when so,
// whether it is SQLSTATE 23505 (unique_violation). The column hint is
// pq.Error.Column when PostgreSQL populated it, falling back to the
// constraint name (most real schemas only carry the latter).
//
// A *pq.Error that is not 23505 is matched-but-not-unique so the caller does
// not misread e.g. a foreign-key or NOT NULL violation as a unique one.
func classifyUniqueViolation(err error) (columnHint string, isUnique bool, matched bool) {
	var pgErr *pq.Error
	if !errors.As(err, &pgErr) {
		return "", false, false
	}
	if pgErr.Code != "23505" {
		return "", false, true
	}
	if pgErr.Column != "" {
		return pgErr.Column, true, true
	}
	return pgErr.Constraint, true, true
}
