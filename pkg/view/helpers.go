package view

import (
	"net/http"

	"github.com/romsar/gonertia"
)

// VelocityHelpers provides Velocity-specific enhancements to gonertia
type VelocityHelpers struct {
	inertia *gonertia.Inertia
}

// Helpers returns a new VelocityHelpers instance
func Helpers() *VelocityHelpers {
	return &VelocityHelpers{
		inertia: Inertia(),
	}
}

// WithErrors renders with validation errors
func WithErrors(w http.ResponseWriter, r *http.Request, component string, props gonertia.Props, errors map[string]interface{}) error {
	// Set errors in context for gonertia to pick up
	ctx := gonertia.SetValidationErrors(r.Context(), errors)
	r = r.WithContext(ctx)

	return Render(w, r, component, props)
}

// WithFlash renders with flash message
func WithFlash(w http.ResponseWriter, r *http.Request, component string, props gonertia.Props, flash map[string]interface{}) error {
	// Note: gonertia handles flash through FlashProvider, not context
	// For now, we'll add flash as props
	if props == nil {
		props = gonertia.Props{}
	}
	for k, v := range flash {
		props["flash_"+k] = v
	}

	return Render(w, r, component, props)
}

// Success redirects with a success message
func Success(w http.ResponseWriter, r *http.Request, message string, url string) {
	// Note: Flash messages need to be handled through session/FlashProvider
	// For now, we'll just redirect
	http.Redirect(w, r, url, http.StatusSeeOther)
}

// Error redirects with an error message
func Error(w http.ResponseWriter, r *http.Request, message string, url string) {
	// Note: Flash messages need to be handled through session/FlashProvider
	// For now, we'll just redirect
	http.Redirect(w, r, url, http.StatusSeeOther)
}

// FormError redirects back with form validation errors
func FormError(w http.ResponseWriter, r *http.Request, errors map[string]interface{}) {
	// Set validation errors in context
	ctx := gonertia.SetValidationErrors(r.Context(), errors)
	r = r.WithContext(ctx)

	// Redirect back
	Back(w, r)
}

// Optional creates an optional prop that only loads on direct visits
func Optional(value interface{}) gonertia.OptionalProp {
	return gonertia.Optional(value)
}

// Always creates a prop that always loads (even on partial reloads)
func Always(value interface{}) gonertia.AlwaysProp {
	return gonertia.Always(value)
}

// Defer creates a deferred prop that loads after the initial response
func Defer(value interface{}, group ...string) gonertia.DeferProp {
	return gonertia.Defer(value, group...)
}

// LazyProp creates a lazy prop that only evaluates when needed
// Note: In gonertia, LazyProp is an alias for OptionalProp
func LazyProp(value interface{}) gonertia.OptionalProp {
	return gonertia.Optional(value)
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
