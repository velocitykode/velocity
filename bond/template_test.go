package bond

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
)

func TestParseTemplateWithFuncs_RegistersFunc(t *testing.T) {
	tmpl, err := parseTemplateWithFuncs(
		`<html><body>{{ shout "hi" }} {{ .inertia }}</body></html>`,
		template.FuncMap{
			"shout": func(s string) string { return s + "!" },
		},
	)
	if err != nil {
		t.Fatalf("parseTemplateWithFuncs failed: %v", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, map[string]any{"inertia": ""}); err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if got := buf.String(); !strings.Contains(got, "hi!") {
		t.Errorf("expected output to contain 'hi!', got: %s", got)
	}
}

func TestNew_PropagatesFuncsIntoTemplate(t *testing.T) {
	b, err := New(Config{
		RootTemplate: `<html><body>{{ shout "hi" }} {{ .inertia }}</body></html>`,
		Funcs: template.FuncMap{
			"shout": func(s string) string { return s + "!" },
		},
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	var buf bytes.Buffer
	if err := b.template.Execute(&buf, map[string]any{"inertia": ""}); err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if got := buf.String(); !strings.Contains(got, "hi!") {
		t.Errorf("expected output to contain 'hi!', got: %s", got)
	}
}

func TestParseTemplate_Valid(t *testing.T) {
	tmpl, err := parseTemplate(`<!DOCTYPE html>
<html>
<body>{{ .inertia }}</body>
</html>`)

	if err != nil {
		t.Fatalf("parseTemplate failed: %v", err)
	}
	if tmpl == nil {
		t.Error("expected template, got nil")
	}
}

func TestParseTemplate_WithInertiaHead(t *testing.T) {
	tmpl, err := parseTemplate(`<!DOCTYPE html>
<html>
<head>{{ .inertiaHead }}</head>
<body>{{ .inertia }}</body>
</html>`)

	if err != nil {
		t.Fatalf("parseTemplate failed: %v", err)
	}
	if tmpl == nil {
		t.Error("expected template, got nil")
	}
}

func TestParseTemplate_EmptyString_ReturnsError(t *testing.T) {
	_, err := parseTemplate("")

	if err != ErrTemplateRequired {
		t.Errorf("expected ErrTemplateRequired, got %v", err)
	}
}

func TestParseTemplate_MissingInertiaPlaceholder_ReturnsError(t *testing.T) {
	_, err := parseTemplate(`<!DOCTYPE html>
<html>
<body>No placeholder here</body>
</html>`)

	if err != ErrInvalidTemplate {
		t.Errorf("expected ErrInvalidTemplate, got %v", err)
	}
}

func TestParseTemplate_InvalidSyntax_ReturnsError(t *testing.T) {
	_, err := parseTemplate(`<!DOCTYPE html>
<html>
<body>{{ .inertia }{{ unclosed</body>
</html>`)

	if err == nil {
		t.Error("expected error for invalid template syntax")
	}
}

func TestParseTemplate_CanExecute(t *testing.T) {
	tmpl, err := parseTemplate(`<!DOCTYPE html>
<html>
<head>{{ .inertiaHead }}</head>
<body>{{ .inertia }}</body>
</html>`)

	if err != nil {
		t.Fatalf("parseTemplate failed: %v", err)
	}

	var buf bytes.Buffer
	data := map[string]template.HTML{
		"inertia":     template.HTML(`<div id="app" data-page='{"test":true}'></div>`),
		"inertiaHead": template.HTML(""),
	}

	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("template execution failed: %v", err)
	}

	result := buf.String()
	if result == "" {
		t.Error("expected non-empty result")
	}
}

func TestParseTemplate_ExecutesWithData(t *testing.T) {
	tmpl, err := parseTemplate(`<!DOCTYPE html>
<html>
<head>{{ .inertiaHead }}</head>
<body>{{ .inertia }}</body>
</html>`)

	if err != nil {
		t.Fatalf("parseTemplate failed: %v", err)
	}

	var buf bytes.Buffer
	data := map[string]template.HTML{
		"inertia":     template.HTML(`<div id="app" data-page='{"component":"Test"}'></div>`),
		"inertiaHead": template.HTML(`<title>Test Page</title>`),
	}

	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("template execution failed: %v", err)
	}

	result := buf.String()

	if !bytes.Contains([]byte(result), []byte(`<div id="app"`)) {
		t.Error("expected inertia div in output")
	}
	if !bytes.Contains([]byte(result), []byte(`<title>Test Page</title>`)) {
		t.Error("expected inertiaHead content in output")
	}
}

func TestParseTemplate_WithCustomFields(t *testing.T) {
	tmpl, err := parseTemplate(`<!DOCTYPE html>
<html>
<head>
{{ .inertiaHead }}
<meta name="csrf-token" content="{{ .csrfToken }}">
</head>
<body>{{ .inertia }}</body>
</html>`)

	if err != nil {
		t.Fatalf("parseTemplate failed: %v", err)
	}

	var buf bytes.Buffer
	data := map[string]any{
		"inertia":     template.HTML(`<div id="app"></div>`),
		"inertiaHead": template.HTML(""),
		"csrfToken":   "abc123",
	}

	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("template execution failed: %v", err)
	}

	result := buf.String()
	if !bytes.Contains([]byte(result), []byte(`content="abc123"`)) {
		t.Error("expected csrfToken in output")
	}
}

func TestTemplateData_Type(t *testing.T) {
	data := TemplateData{
		Inertia:     template.HTML(`<div id="app"></div>`),
		InertiaHead: template.HTML(`<title>Test</title>`),
	}

	if data.Inertia == "" {
		t.Error("expected non-empty Inertia")
	}
	if data.InertiaHead == "" {
		t.Error("expected non-empty InertiaHead")
	}
}

func TestParseTemplate_MinimalValid(t *testing.T) {
	// Minimum valid template - just needs .inertia somewhere
	tmpl, err := parseTemplate(`{{ .inertia }}`)

	if err != nil {
		t.Fatalf("parseTemplate failed: %v", err)
	}
	if tmpl == nil {
		t.Error("expected template, got nil")
	}
}

func TestParseTemplate_InertiaInComment(t *testing.T) {
	// Even in a comment, .inertia is present (validation is simple string check)
	tmpl, err := parseTemplate(`<!-- {{ .inertia }} -->`)

	if err != nil {
		t.Fatalf("parseTemplate failed: %v", err)
	}
	if tmpl == nil {
		t.Error("expected template, got nil")
	}
}
