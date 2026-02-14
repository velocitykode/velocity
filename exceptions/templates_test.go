package exceptions

import (
	"strings"
	"testing"
)

func TestDebugTemplateHTML(t *testing.T) {
	if debugTemplateHTML == "" {
		t.Error("debugTemplateHTML is empty")
	}

	// Check essential elements
	checks := []string{
		"<!DOCTYPE html>",
		"{{.StatusCode}}",
		"{{.Message}}",
		"{{.ExceptionType}}",
		"Stack Trace",
		"{{range $i, $frame := .Frames}}",
		"Exception Context",
		"Previous Exception",
	}

	for _, check := range checks {
		if !strings.Contains(debugTemplateHTML, check) {
			t.Errorf("debugTemplateHTML missing: %s", check)
		}
	}
}

func TestErrorTemplateHTML(t *testing.T) {
	if errorTemplateHTML == "" {
		t.Error("errorTemplateHTML is empty")
	}

	// Check essential elements
	checks := []string{
		"<!DOCTYPE html>",
		"{{.StatusCode}}",
		"{{.StatusText}}",
		"{{.Message}}",
		"Go Home",
	}

	for _, check := range checks {
		if !strings.Contains(errorTemplateHTML, check) {
			t.Errorf("errorTemplateHTML missing: %s", check)
		}
	}
}

func TestNotFoundTemplateHTML(t *testing.T) {
	if notFoundTemplateHTML == "" {
		t.Error("notFoundTemplateHTML is empty")
	}

	// Check essential elements
	checks := []string{
		"<!DOCTYPE html>",
		"404",
		"Page Not Found",
	}

	for _, check := range checks {
		if !strings.Contains(notFoundTemplateHTML, check) {
			t.Errorf("notFoundTemplateHTML missing: %s", check)
		}
	}
}

func TestServerErrorTemplateHTML(t *testing.T) {
	if serverErrorTemplateHTML == "" {
		t.Error("serverErrorTemplateHTML is empty")
	}

	// Check essential elements
	checks := []string{
		"<!DOCTYPE html>",
		"500",
		"Server Error",
	}

	for _, check := range checks {
		if !strings.Contains(serverErrorTemplateHTML, check) {
			t.Errorf("serverErrorTemplateHTML missing: %s", check)
		}
	}
}

func TestTemplatesAreValidHTML(t *testing.T) {
	templates := map[string]string{
		"debug":       debugTemplateHTML,
		"error":       errorTemplateHTML,
		"notFound":    notFoundTemplateHTML,
		"serverError": serverErrorTemplateHTML,
	}

	for name, tpl := range templates {
		t.Run(name, func(t *testing.T) {
			// Basic HTML structure checks
			if !strings.HasPrefix(tpl, "<!DOCTYPE html>") {
				t.Error("Should start with DOCTYPE")
			}
			if !strings.Contains(tpl, "<html") {
				t.Error("Should have html tag")
			}
			if !strings.Contains(tpl, "<head>") {
				t.Error("Should have head tag")
			}
			if !strings.Contains(tpl, "<body>") {
				t.Error("Should have body tag")
			}
			if !strings.Contains(tpl, "</html>") {
				t.Error("Should have closing html tag")
			}
		})
	}
}
