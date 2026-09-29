// Package sqlerr describes a database error for a framework log line
// without its text.
//
// Database drivers write the offending value into their error text (a
// Postgres "Key (email)=(...)" detail, a MySQL "Duplicate entry '...'"), so
// the framework's own log lines about a failed statement never print that
// text; they print Kind instead, and the error itself still goes back to the
// caller. The rule matches the statement log (orm/drivers), which leaves
// failure text out for the same reason.
package sqlerr

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"reflect"
)

// Key is the log key Kind is written under.
const Key = "error_kind"

// Other is the kind of an error that is neither a listed sentinel nor a
// listed driver error, including one whose chain could not be walked.
const Other = "other"

// maxChain bounds how many errors of one chain Kind looks at, so a chain
// that loops back on itself still ends.
const maxChain = 32

// sentinels are the value-free errors Kind names by their identifier, in
// priority order: when a chain holds several, the first listed wins.
var sentinels = []struct {
	err  error
	name string
}{
	{context.Canceled, "context.Canceled"},
	{context.DeadlineExceeded, "context.DeadlineExceeded"},
	{sql.ErrTxDone, "sql.ErrTxDone"},
	{sql.ErrConnDone, "sql.ErrConnDone"},
	{sql.ErrNoRows, "sql.ErrNoRows"},
	{driver.ErrBadConn, "driver.ErrBadConn"},
}

// driverErrors are the error types of the database drivers the framework
// ships, matched by package path and type name, so the package imports no
// driver. Only a named type can match, and the answer is the entry's fixed
// kind, never a string built from the error's own type.
var driverErrors = []struct {
	pkgPath, name string
	pointer       bool
	kind          string
}{
	{"github.com/lib/pq", "Error", true, "*pq.Error"},
	{"github.com/go-sql-driver/mysql", "MySQLError", true, "*mysql.MySQLError"},
	{"github.com/mattn/go-sqlite3", "Error", false, "sqlite3.Error"},
	{"modernc.org/sqlite", "Error", true, "*sqlite.Error"},
}

// Kind names what err is without saying what it holds, from a fixed set:
// the identifier of a listed sentinel anywhere in its chain (see
// sentinels), else the kind of the first listed driver error in its chain
// (see driverErrors), else Other. It returns "" for nil.
//
// The chain is walked once, breadth first through Unwrap() error and
// Unwrap() []error, looking at no more than maxChain errors, so a chain
// that loops back on itself ends and a loop in one branch of a join does
// not hide the others. A sentinel is matched by identity or by
// the error's own Is method. An Unwrap or Is that panics makes the answer
// Other: Kind runs on failure lines inside cleanup, which must finish.
func Kind(err error) (kind string) {
	if err == nil {
		return ""
	}
	defer func() {
		if recover() != nil {
			kind = Other
		}
	}()
	best := len(sentinels)
	driverKind := ""
	queue := []error{err}
	for seen := 0; seen < len(queue) && seen < maxChain; seen++ {
		e := queue[seen]
		if e == nil {
			continue
		}
		for i := 0; i < best; i++ {
			if matches(e, sentinels[i].err) {
				best = i
				break
			}
		}
		if driverKind == "" {
			driverKind = driverKindOf(e)
		}
		var next []error
		switch u := e.(type) {
		case interface{ Unwrap() error }:
			next = []error{u.Unwrap()}
		case interface{ Unwrap() []error }:
			next = u.Unwrap()
		}
		room := max(maxChain-len(queue), 0)
		queue = append(queue, next[:min(len(next), room)]...)
	}
	switch {
	case best < len(sentinels):
		return sentinels[best].name
	case driverKind != "":
		return driverKind
	default:
		return Other
	}
}

// matches reports whether e is target, by identity or by e's own Is
// method, without following e's chain (Kind walks it).
func matches(e, target error) bool {
	if reflect.TypeOf(e).Comparable() && e == target {
		return true
	}
	if x, ok := e.(interface{ Is(error) bool }); ok {
		return x.Is(target)
	}
	return false
}

// driverKindOf returns the fixed kind of e's type when it is a listed
// driver error, else "".
func driverKindOf(e error) string {
	t := reflect.TypeOf(e)
	pointer := t.Kind() == reflect.Pointer
	if pointer {
		t = t.Elem()
	}
	for _, d := range driverErrors {
		if d.pointer == pointer && t.Name() == d.name && t.PkgPath() == d.pkgPath {
			return d.kind
		}
	}
	return ""
}
