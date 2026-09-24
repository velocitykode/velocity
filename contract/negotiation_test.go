package contract

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWantsJSON(t *testing.T) {
	tests := []struct {
		name    string
		accept  string
		xhr     string
		inertia string
		want    bool
	}{
		{"no headers", "", "", "", false},
		{"json", "application/json", "", "", true},
		{"json first", "application/json, text/html", "", "", true},
		{"html first then json", "text/html, application/json", "", "", false},
		{"browser default", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8", "", "", false},
		{"json with parameters", "application/json; charset=utf-8", "", "", true},
		{"json with q", "application/json;q=0.9, text/html", "", "", true},
		{"case and spaces", "  Application/JSON  ", "", "", true},
		{"problem json suffix", "application/problem+json", "", "", true},
		{"vendor json suffix", "application/vnd.api+json;q=1, text/html", "", "", true},
		{"json suffix second", "text/html, application/problem+json", "", "", false},
		{"json-like subtype", "application/jsonx", "", "", false},
		{"xml", "application/xml", "", "", false},
		{"wildcard alone", "*/*", "", "", false},
		{"xhr empty accept", "", "XMLHttpRequest", "", true},
		{"xhr wildcard accept", "*/*", "XMLHttpRequest", "", true},
		{"xhr wildcard first", "*/*, text/html", "XMLHttpRequest", "", true},
		{"xhr wildcard with q", "*/*;q=0.8", "XMLHttpRequest", "", true},
		{"xhr lower case", "", "xmlhttprequest", "", true},
		{"xhr html accept", "text/html", "XMLHttpRequest", "", false},
		{"xhr other value", "", "fetch", "", false},
		{"inertia json accept", "application/json", "", "true", false},
		{"inertia xhr", "", "XMLHttpRequest", "true", false},
		{"inertia problem json", "application/problem+json", "XMLHttpRequest", "true", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.accept != "" {
				r.Header.Set("Accept", tt.accept)
			}
			if tt.xhr != "" {
				r.Header.Set("X-Requested-With", tt.xhr)
			}
			if tt.inertia != "" {
				r.Header.Set("X-Inertia", tt.inertia)
			}
			if got := WantsJSON(r); got != tt.want {
				t.Errorf("WantsJSON() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWantsJSON_NilRequest(t *testing.T) {
	if WantsJSON(nil) {
		t.Error("WantsJSON(nil) = true, want false")
	}
}

func TestIsInertia(t *testing.T) {
	tests := []struct {
		name  string
		value string
		set   bool
		want  bool
	}{
		{"present", "true", true, true},
		{"any value", "1", true, true},
		{"empty value", "", true, false},
		{"absent", "", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.set {
				r.Header["X-Inertia"] = []string{tt.value}
			}
			if got := IsInertia(r); got != tt.want {
				t.Errorf("IsInertia() = %v, want %v", got, tt.want)
			}
		})
	}
	if IsInertia(nil) {
		t.Error("IsInertia(nil) = true, want false")
	}
}
