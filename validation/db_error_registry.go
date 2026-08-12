package validation

import "sync"

// UniqueViolationClassifier inspects a database error and reports whether it
// is a driver-typed UNIQUE-constraint violation.
//
// Return values:
//
//   - columnHint: the offending column or index name when the driver exposes
//     one (may be empty);
//   - isUnique: true when err is a UNIQUE-constraint violation;
//   - matched: true when the classifier recognised err as belonging to its
//     own driver, whatever the constraint kind. When matched is true the
//     result is authoritative: no further classifier and no generic string
//     fallback runs. When matched is false the classifier did not recognise
//     err and the next classifier (or the string fallback) is consulted.
//
// The matched/isUnique split lets a driver classifier say "this is my error
// and it is NOT a unique violation" (e.g. a foreign-key failure: matched=true,
// isUnique=false). That suppresses the string fallback so an FK error is never
// misread as a unique one.
//
// This seam keeps the core validation package free of any SQL-driver import.
// The typed *pq.Error / *mysql.MySQLError classifiers live in the orm/postgres
// and orm/mysql leaf packages and register themselves from init(); they are
// only linked into binaries that import those leaves (directly or via
// orm/standard). Apps that do not wire a driver fall through to the generic
// error-string matching in validation/dbrules.
type UniqueViolationClassifier func(err error) (columnHint string, isUnique bool, matched bool)

var (
	classifierMu      sync.RWMutex
	uniqueClassifiers []UniqueViolationClassifier
)

// RegisterUniqueViolationClassifier appends a driver-specific classifier to
// the registry. Driver leaf packages (orm/postgres, orm/mysql) call this from
// init() so typed UNIQUE-violation matching is available to AsValidationError
// without the core validation package importing any SQL driver. A nil
// classifier is ignored. Safe for concurrent use.
func RegisterUniqueViolationClassifier(c UniqueViolationClassifier) {
	if c == nil {
		return
	}
	classifierMu.Lock()
	uniqueClassifiers = append(uniqueClassifiers, c)
	classifierMu.Unlock()
}

// ClassifyUniqueViolation runs the registered classifiers in registration
// order and returns the first authoritative (matched) result. It reports
// matched=false when no classifier recognises err, signalling the caller to
// fall back to generic error-string matching. Safe for concurrent use.
func ClassifyUniqueViolation(err error) (columnHint string, isUnique bool, matched bool) {
	classifierMu.RLock()
	defer classifierMu.RUnlock()
	for _, c := range uniqueClassifiers {
		if hint, isUnique, ok := c(err); ok {
			return hint, isUnique, true
		}
	}
	return "", false, false
}
