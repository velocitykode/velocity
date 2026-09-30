// Package errchain stands in for the module's contained helpers: its own
// calls are out of scope.
package errchain

import "errors"

func Is(err, target error) bool { return errors.Is(err, target) }

func Text(err error) string { return err.Error() }
