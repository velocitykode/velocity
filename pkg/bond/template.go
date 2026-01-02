package bond

import (
	"html/template"
	"strings"
)

// parseTemplate parses and validates the root HTML template
func parseTemplate(templateStr string) (*template.Template, error) {
	if templateStr == "" {
		return nil, ErrTemplateRequired
	}

	// Validate required placeholder exists
	if !strings.Contains(templateStr, ".inertia") {
		return nil, ErrInvalidTemplate
	}

	return template.New("root").Parse(templateStr)
}

// TemplateData holds data passed to the root HTML template
type TemplateData struct {
	Inertia     template.HTML // The Inertia div with data-page attribute
	InertiaHead template.HTML // Head content for SSR (empty for CSR)
}
