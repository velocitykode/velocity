package mysql

import (
	"errors"
	"strings"

	drivermysql "github.com/go-sql-driver/mysql"

	"github.com/velocitykode/velocity/validation"
)

// init registers a typed UNIQUE-violation classifier with the core validation
// package so validation/dbrules.AsValidationError can attribute a MySQL
// unique-constraint race to the right field via *mysql.MySQLError instead of
// error string matching. Importing this leaf (directly or via orm/standard)
// wires it; the core validation package keeps no go-sql-driver dependency of
// its own.
func init() {
	validation.RegisterUniqueViolationClassifier(classifyUniqueViolation)
}

// classifyUniqueViolation reports whether err is a *mysql.MySQLError and, when
// so, whether it is errno 1062 (ER_DUP_ENTRY) or 1586
// (ER_DUP_ENTRY_WITH_KEY_NAME). The hint is the index name MySQL embeds in the
// message as `for key '<name>'`; callers match it against field names.
//
// A *mysql.MySQLError with any other errno is matched-but-not-unique so the
// caller does not misread e.g. a foreign-key violation as a unique one.
func classifyUniqueViolation(err error) (columnHint string, isUnique bool, matched bool) {
	var myErr *drivermysql.MySQLError
	if !errors.As(err, &myErr) {
		return "", false, false
	}
	if myErr.Number != 1062 && myErr.Number != 1586 {
		return "", false, true
	}
	return extractKeyName(myErr.Message), true, true
}

// extractKeyName pulls the index name out of a MySQL ER_DUP_ENTRY message of
// the form `Duplicate entry 'val' for key 'idx_name'`. Returns "" when the
// pattern is absent; callers tolerate an empty hint.
func extractKeyName(msg string) string {
	const marker = "for key '"
	i := strings.Index(msg, marker)
	if i < 0 {
		return ""
	}
	rest := msg[i+len(marker):]
	j := strings.Index(rest, "'")
	if j < 0 {
		return ""
	}
	return rest[:j]
}
