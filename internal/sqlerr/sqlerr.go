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
	"errors"
	"fmt"
)

// Key is the log key Kind is written under.
const Key = "error_kind"

// sentinels are the value-free errors Kind names by their identifier rather
// than by their type, which (an *errors.errorString) would not tell them
// apart.
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

// Kind names what err is without saying what it holds: the identifier of a
// well-known sentinel anywhere in its chain, otherwise the Go type of the
// innermost error in its chain (the driver's own type, such as *pq.Error or
// *mysql.MySQLError, beneath any wrapping). It returns "" for nil.
func Kind(err error) string {
	if err == nil {
		return ""
	}
	for _, s := range sentinels {
		if errors.Is(err, s.err) {
			return s.name
		}
	}
	return fmt.Sprintf("%T", innermost(err))
}

// innermost follows err's Unwrap chain to its end, taking the first branch
// of an error that joins several.
func innermost(err error) error {
	for {
		var next error
		switch u := err.(type) {
		case interface{ Unwrap() error }:
			next = u.Unwrap()
		case interface{ Unwrap() []error }:
			if errs := u.Unwrap(); len(errs) > 0 {
				next = errs[0]
			}
		}
		if next == nil {
			return err
		}
		err = next
	}
}
