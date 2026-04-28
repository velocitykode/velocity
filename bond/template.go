package bond

import (
	"html/template"
	"strings"
)

// parseTemplate parses and validates the root HTML template.
func parseTemplate(templateStr string) (*template.Template, error) {
	return parseTemplateWithFuncs(templateStr, nil)
}

// parseTemplateWithFuncs is parseTemplate plus a FuncMap registered
// before parsing so the template can call helpers like
// {{ vite "..." }}. funcs may be nil.
func parseTemplateWithFuncs(templateStr string, funcs template.FuncMap) (*template.Template, error) {
	if templateStr == "" {
		return nil, ErrTemplateRequired
	}

	// Validate required placeholder exists
	if !strings.Contains(templateStr, ".inertia") {
		return nil, ErrInvalidTemplate
	}

	tmpl := template.New("root")
	if funcs != nil {
		tmpl = tmpl.Funcs(funcs)
	}
	return tmpl.Parse(templateStr)
}

// TemplateData holds data passed to the root HTML template
type TemplateData struct {
	Inertia     template.HTML // The Inertia div with data-page attribute
	InertiaHead template.HTML // Head content for SSR (empty for CSR)
}
