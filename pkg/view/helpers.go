package view

import (
	"net/http"

	"github.com/velocitykode/velocity/pkg/bond"
)

// Success redirects with a success status
func Success(w http.ResponseWriter, r *http.Request, message string, url string) {
	// Note: Flash messages need to be handled through session
	// For now, we'll just redirect
	http.Redirect(w, r, url, http.StatusSeeOther)
}

// Error redirects with an error status
func Error(w http.ResponseWriter, r *http.Request, message string, url string) {
	// Note: Flash messages need to be handled through session
	// For now, we'll just redirect
	http.Redirect(w, r, url, http.StatusSeeOther)
}

// Lazy creates a lazy prop that only evaluates when explicitly requested
func Lazy(fn func() (any, error)) bond.LazyProp {
	return bond.Lazy(fn)
}

// Optional creates an optional prop (alias for Lazy)
func Optional(fn func() (any, error)) bond.OptionalProp {
	return bond.Optional(fn)
}

// Always creates a prop that always loads (even on partial reloads)
func Always(value any) bond.AlwaysProp {
	return bond.Always(value)
}

// Defer creates a deferred prop that loads after the initial response
func Defer(fn func() (any, error), group ...string) bond.DeferredProp {
	return bond.Defer(fn, group...)
}

// LazyProp creates a lazy prop that only evaluates when needed
// Note: This is an alias for Optional for backwards compatibility
func LazyProp(value any) bond.OptionalProp {
	return bond.Optional(func() (any, error) {
		return value, nil
	})
}

// SimpleFlashProvider is a basic flash message provider
type SimpleFlashProvider struct {
	messages map[string]interface{}
}

// NewSimpleFlashProvider creates a new flash provider
func NewSimpleFlashProvider() *SimpleFlashProvider {
	return &SimpleFlashProvider{
		messages: make(map[string]interface{}),
	}
}

// Set sets a flash message
func (p *SimpleFlashProvider) Set(key string, value interface{}) {
	p.messages[key] = value
}

// GetFlashData returns all flash messages
func (p *SimpleFlashProvider) GetFlashData(r *http.Request) (map[string]interface{}, error) {
	// In production, this would read from session
	flash := p.messages
	p.messages = make(map[string]interface{}) // Clear after reading
	return flash, nil
}

// SimpleValidationProvider is a basic validation errors provider
type SimpleValidationProvider struct {
	errors map[string]interface{}
}

// NewSimpleValidationProvider creates a new validation provider
func NewSimpleValidationProvider() *SimpleValidationProvider {
	return &SimpleValidationProvider{
		errors: make(map[string]interface{}),
	}
}

// Set sets validation errors
func (p *SimpleValidationProvider) Set(errors map[string]interface{}) {
	p.errors = errors
}

// GetValidationErrors returns validation errors
func (p *SimpleValidationProvider) GetValidationErrors(r *http.Request) (map[string]interface{}, error) {
	// In production, this would read from session
	errors := p.errors
	p.errors = make(map[string]interface{}) // Clear after reading
	return errors, nil
}

// GetOldInput returns old form input (preserved on validation errors)
func (p *SimpleValidationProvider) GetOldInput(r *http.Request) (map[string]interface{}, error) {
	// In production, this would read from session
	return map[string]interface{}{}, nil
}
