package contract

import "fmt"

// RegistrationError is a typed error for registration-time failures.
// Methods that cannot return error (e.g. Router.Get) panic with this type
// so misuse is loud at bootstrap and debuggable with recover.
type RegistrationError struct {
	Package string
	Message string
}

func (e *RegistrationError) Error() string {
	return fmt.Sprintf("velocity/%s: %s", e.Package, e.Message)
}

// NewRegistrationError creates a new RegistrationError.
func NewRegistrationError(pkg, msg string) *RegistrationError {
	return &RegistrationError{Package: pkg, Message: msg}
}
