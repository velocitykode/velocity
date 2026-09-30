// Package errchain stands in for the module's internal/errchain: its
// formatting entries call the operands' methods in their bodies, as the
// real ones do on a nested panic.
package errchain

import "fmt"

func Errorf(format string, args ...any) error {
	if e, ok := args[0].(error); ok {
		_ = e.Error()
	}
	return fmt.Errorf(format, args...)
}

func Sprintf(format string, args ...any) string {
	if s, ok := args[0].(fmt.Stringer); ok {
		return s.String()
	}
	return fmt.Sprintf(format, args...)
}

func Sprint(v any) string {
	if s, ok := v.(fmt.Stringer); ok {
		return s.String()
	}
	return fmt.Sprint(v)
}

func Text(err error) string { return err.Error() }
