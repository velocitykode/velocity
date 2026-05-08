package driverregistry

import (
	"errors"
	"fmt"
	"strings"
)

// ErrDriverNotFound is the sentinel returned (wrapped in *NotFoundError) when
// Resolve cannot find a registered driver. Callers can match on this with
// errors.Is to handle "no such driver" generically without importing the
// concrete error type.
var ErrDriverNotFound = errors.New("driverregistry: driver not registered")

// NotFoundError is returned by Registry.Resolve when the requested driver
// name has no registered factory. It carries the subsystem name (so the
// message reads e.g. "velocity/cache: ...") and the list of available
// drivers (sorted) so the error message can guide the caller toward a
// valid choice without an extra introspection round trip.
type NotFoundError struct {
	Subsystem string
	Name      string
	Available []string
}

func (e *NotFoundError) Error() string {
	if len(e.Available) == 0 {
		return fmt.Sprintf("velocity/%s: driver %q not registered (no drivers registered, did you forget the blank import?)", e.Subsystem, e.Name)
	}
	return fmt.Sprintf("velocity/%s: driver %q not registered (available: %s)", e.Subsystem, e.Name, strings.Join(e.Available, ", "))
}

// Unwrap lets errors.Is(err, ErrDriverNotFound) succeed so callers can
// branch on the sentinel without losing the structured fields.
func (e *NotFoundError) Unwrap() error { return ErrDriverNotFound }
